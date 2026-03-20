#include <linux/delay.h>
#include <linux/errno.h>
#include <linux/jiffies.h>
#include <linux/kernel.h>
#include <linux/kthread.h>
#include <linux/ktime.h>
#include <linux/module.h>
#include <linux/smp.h>
#include <linux/string.h>
#include <linux/types.h>

#include "mkring.h"

/*
 * mkring loadable test module
 *
 * 数据传递函数调用链（测试路径）：
 *
 * 1) 前置条件（mkring 主体已内置并完成初始化）
 *    kernel cmdline -> init_mk.c(__setup) -> subsys_initcall(init_mk)
 *    -> mkring_init()
 *       -> mkring_calc_layout()
 *       -> request_mem_region()/memremap()
 *       -> mkring_prepare_shared()
 *       -> mkring_queue_setup()
 *
 * 2) 测试模块加载
 *    insmod mkring_test.ko peer=<peer_id> period_ms=<ms> report_sec=<sec>
 *    -> module_init(mkring_test_init)
 *       -> mkring_register_rx_cb(peer, mk_test_rx_cb, ...)
 *       -> kthread_run(mk_test_tx_thread)
 *
 * 3) 发送路径（本侧）
 *    mk_test_tx_thread
 *    -> mkring_send(peer, &msg, sizeof(msg))
 *       -> mkring_tx_enqueue(txq[peer], ...)
 *          -> mkring_tx_reclaim_locked()   (先扫 used 回收旧 desc)
 *          -> 写 data/desc.len/avail ring
 *       -> notify(src, dst, priv)          (IPI 通知回调)
 *          -> 默认后端 mk_ipi_notify()
 *             -> apic_icr_write(... vector, apic_id)
 *
 * 4) 中断与接收路径（对侧）
 *    平台 IPI vector 入口
 *    -> mkring_ipi_interrupt()
 *       -> mkring_handle_ipi_all()
 *          -> mkring_process_rx_queue(rxq[src])
 *             -> 读 avail ring / desc / data
 *             -> 写 used ring (回执给发送侧回收)
 *             -> mkring_rx_enqueue_local()
 *                -> 调用已注册回调 mk_test_rx_cb()
 *
 * 5) 回收闭环（发送侧下一次发送前）
 *    mkring_tx_reclaim_locked() 读取 used ring
 *    -> 清 inflight 位图，desc 重新可分配。
 *
 * 6) 测试模块卸载
 *    rmmod mkring_test
 *    -> module_exit(mkring_test_exit)
 *       -> kthread_stop(tx_thread)
 *       -> mkring_unregister_rx_cb(peer)
 *
 * 测试模块函数职责一览：
 *
 * - mk_test_report(ctx, tag)
 *   统一打印统计信息（tx_ok/tx_fail/rx_ok/rx_bad），用于周期观测和退出收口。
 *
 * - mk_test_rx_cb(src_kid, data, len, priv)
 *   接收回调函数。由 mkring 在接收路径中调用，执行轻量校验：
 *   1) 源 kernel 是否为配置的 peer
 *   2) 消息长度是否匹配 struct mkring_test_msg
 *   3) magic 是否为 MKRING_TEST_MAGIC
 *   校验通过计入 rx_ok，失败计入 rx_bad。该函数运行在中断上下文，不能睡眠。
 *
 * - mk_test_tx_thread(arg)
 *   发送线程主循环（进程上下文）：
 *   1) 构造测试消息（magic/seq/timestamp/cpu）
 *   2) 调用 mkring_send() 向 peer 发送
 *   3) 统计 tx_ok/tx_fail
 *   4) 按 report_sec 周期打印统计，按 period_ms 周期发送
 *   线程在 kthread_should_stop() 置位后退出。
 *
 * - mkring_test_init()
 *   模块加载入口（insmod 时执行）：
 *   1) 校验模块参数（peer/period_ms/report_sec）
 *   2) 注册 mkring_register_rx_cb()
 *   3) 启动发送线程 kthread_run()
 *   任一步失败会执行必要回滚并返回错误码，insmod 失败。
 *
 * - mkring_test_exit()
 *   模块卸载入口（rmmod 时执行）：
 *   1) 停止发送线程
 *   2) 注销接收回调
 *   3) 打印最终统计并输出卸载日志
 *
 * - module_init(mkring_test_init) / module_exit(mkring_test_exit)
 *   将上述两个入口分别绑定到模块加载/卸载生命周期。
 */

#define MKRING_TEST_MAGIC	0x4d4b5453U /* "MKTS" */
#define MKRING_TEST_NAME	"mkring-test"

static int peer = -1;
module_param(peer, int, 0444);
MODULE_PARM_DESC(peer, "Remote kernel_id to send to/receive from");

static uint period_ms = 1000;
module_param(period_ms, uint, 0644);
MODULE_PARM_DESC(period_ms, "TX period in milliseconds");

static uint report_sec = 5;
module_param(report_sec, uint, 0644);
MODULE_PARM_DESC(report_sec, "Periodic report interval in seconds");

struct mkring_test_msg {
	__le32 magic;
	__le32 seq;
	__le64 send_ns;
	__le32 sender_cpu;
	u8 reserved[12];
} __packed;

struct mkring_test_ctx {
	u16 peer;
	u32 period_ms;
	u32 report_sec;
	struct task_struct *tx_task;
	atomic64_t tx_seq;
	atomic64_t tx_ok;
	atomic64_t tx_fail;
	atomic64_t rx_ok;
	atomic64_t rx_bad;
	bool rx_registered;
};

static struct mkring_test_ctx mk_test_ctx = {
	.peer = U16_MAX,
	.period_ms = 1000,
	.report_sec = 5,
};

static void mk_test_report(const struct mkring_test_ctx *ctx, const char *tag)
{
	pr_info("%s: %s peer=%u tx_ok=%lld tx_fail=%lld rx_ok=%lld rx_bad=%lld\n",
		MKRING_TEST_NAME, tag, ctx->peer,
		atomic64_read(&ctx->tx_ok), atomic64_read(&ctx->tx_fail),
		atomic64_read(&ctx->rx_ok), atomic64_read(&ctx->rx_bad));
}

static void mk_test_rx_cb(u16 src_kid, const void *data, u32 len, void *priv)
{
	const struct mkring_test_ctx *ctx = priv;
	const struct mkring_test_msg *msg = data;

	if (!ctx)
		return;

	/*
	 * 回调运行在中断上下文，不能睡眠。
	 * 这里只做轻量校验和计数，避免影响中断处理路径。
	 */
	if (src_kid != ctx->peer || len != sizeof(*msg) ||
	    le32_to_cpu(msg->magic) != MKRING_TEST_MAGIC) {
		atomic64_inc(&ctx->rx_bad);
		return;
	}

	atomic64_inc(&ctx->rx_ok);
}

static int mk_test_tx_thread(void *arg)
{
	struct mkring_test_ctx *ctx = arg;
	unsigned long next_report = jiffies + ctx->report_sec * HZ;

	while (!kthread_should_stop()) {
		struct mkring_test_msg msg = {
			.magic = cpu_to_le32(MKRING_TEST_MAGIC),
			.seq = cpu_to_le32((u32)atomic64_inc_return(&ctx->tx_seq)),
			.send_ns = cpu_to_le64(ktime_get_ns()),
			.sender_cpu = cpu_to_le32((u32)smp_processor_id()),
		};
		int ret;

		ret = mkring_send(ctx->peer, &msg, sizeof(msg));
		if (!ret)
			atomic64_inc(&ctx->tx_ok);
		else
			atomic64_inc(&ctx->tx_fail);

		if (time_after_eq(jiffies, next_report)) {
			mk_test_report(ctx, "periodic");
			next_report = jiffies + ctx->report_sec * HZ;
		}

		if (msleep_interruptible(ctx->period_ms))
			break;
	}

	mk_test_report(ctx, "stop");
	return 0;
}

static int __init mkring_test_init(void)
{
	int ret;

	if (peer < 0 || peer > U16_MAX) {
		pr_err("%s: invalid peer=%d\n", MKRING_TEST_NAME, peer);
		return -EINVAL;
	}
	if (!period_ms || !report_sec) {
		pr_err("%s: period_ms/report_sec must be non-zero\n",
		       MKRING_TEST_NAME);
		return -EINVAL;
	}

	mk_test_ctx.peer = (u16)peer;
	mk_test_ctx.period_ms = period_ms;
	mk_test_ctx.report_sec = report_sec;

	ret = mkring_register_rx_cb(mk_test_ctx.peer, mk_test_rx_cb, &mk_test_ctx);
	if (ret) {
		pr_err("%s: mkring_register_rx_cb(peer=%u) failed: %d\n",
		       MKRING_TEST_NAME, mk_test_ctx.peer, ret);
		return ret;
	}
	mk_test_ctx.rx_registered = true;

	mk_test_ctx.tx_task = kthread_run(mk_test_tx_thread, &mk_test_ctx,
					  "mkring-test-tx");
	if (IS_ERR(mk_test_ctx.tx_task)) {
		ret = PTR_ERR(mk_test_ctx.tx_task);
		mk_test_ctx.tx_task = NULL;
		pr_err("%s: kthread_run failed: %d\n", MKRING_TEST_NAME, ret);
		mkring_unregister_rx_cb(mk_test_ctx.peer);
		mk_test_ctx.rx_registered = false;
		return ret;
	}

	pr_info("%s: started peer=%u period_ms=%u report_sec=%u\n",
		MKRING_TEST_NAME, mk_test_ctx.peer,
		mk_test_ctx.period_ms, mk_test_ctx.report_sec);
	return 0;
}

static void __exit mkring_test_exit(void)
{
	if (mk_test_ctx.tx_task) {
		kthread_stop(mk_test_ctx.tx_task);
		mk_test_ctx.tx_task = NULL;
	}

	if (mk_test_ctx.rx_registered) {
		mkring_unregister_rx_cb(mk_test_ctx.peer);
		mk_test_ctx.rx_registered = false;
	}

	mk_test_report(&mk_test_ctx, "exit");
	pr_info("%s: unloaded\n", MKRING_TEST_NAME);
}

module_init(mkring_test_init);
module_exit(mkring_test_exit);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("yezhucan");
MODULE_DESCRIPTION("Loadable self-test module for mkring");
