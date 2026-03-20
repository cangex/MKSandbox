CC ?= cc
CFLAGS ?= -O2
STATIC ?= 0
BASE_CFLAGS := -std=gnu11 -Wall -Wextra -Werror -pedantic -Iinclude -pthread
LDFLAGS ?=
LDLIBS := -pthread

ifeq ($(STATIC),1)
LDFLAGS += -static
endif

SRC := \
	src/agent.c \
	src/config.c \
	src/main.c \
	src/protocol.c \
	src/runtime_containerd_stub.c \
	src/runtime_memory.c \
	src/transport_device.c \
	src/transport_stub.c

OBJ := $(SRC:.c=.o)
BIN := mk-guest-agent
TEST_BIN := tests/test_agent

.PHONY: all clean test

all: $(BIN)

$(BIN): $(OBJ)
	$(CC) $(BASE_CFLAGS) $(CFLAGS) $(LDFLAGS) -o $@ $(OBJ) $(LDLIBS)

src/%.o: src/%.c
	$(CC) $(BASE_CFLAGS) $(CFLAGS) -c $< -o $@

$(TEST_BIN): tests/test_agent.c \
	src/agent.c \
	src/config.c \
	src/protocol.c \
	src/runtime_containerd_stub.c \
	src/runtime_memory.c \
	src/transport_device.c \
	src/transport_stub.c
	$(CC) $(BASE_CFLAGS) $(CFLAGS) -o $@ \
		tests/test_agent.c \
		src/agent.c \
		src/config.c \
		src/protocol.c \
		src/runtime_containerd_stub.c \
		src/runtime_memory.c \
		src/transport_device.c \
		src/transport_stub.c \
		$(LDFLAGS) \
		$(LDLIBS)

test: $(TEST_BIN)
	./$(TEST_BIN)

clean:
	rm -f $(OBJ) $(BIN) $(TEST_BIN)
