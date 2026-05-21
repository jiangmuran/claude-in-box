# cib_aes — reference C client build.
#
#   make                  # builds libcib_aes.a + cib-example
#   make clean
#   make CFLAGS_EXTRA="-DMYMACRO"
#
# Dependencies (install on your platform):
#   - mbedtls (mbedtls-dev on Debian/Ubuntu, mbedtls via brew on macOS)
#   - libcurl (libcurl4-openssl-dev on Debian/Ubuntu, curl via brew)

CC       ?= cc
AR       ?= ar
CFLAGS   ?= -O2 -g -Wall -Wextra -Wshadow -Wpointer-arith -Wcast-align -Wstrict-prototypes -Wmissing-prototypes -fno-strict-aliasing -std=c11
CFLAGS   += $(CFLAGS_EXTRA)
INC      ?= $(shell pkg-config --cflags-only-I libcurl 2>/dev/null)
LDLIBS   ?= -lmbedcrypto -lcurl

ifeq ($(shell uname -s),Darwin)
  # brew --prefix on Apple Silicon
  BREW_PREFIX ?= $(shell brew --prefix 2>/dev/null)
  ifneq ($(BREW_PREFIX),)
    INC    += -I$(BREW_PREFIX)/include
    LDLIBS += -L$(BREW_PREFIX)/lib
  endif
endif

OBJS    := cib_aes.o
LIB     := libcib_aes.a
EXAMPLE := cib-example

.PHONY: all clean
all: $(LIB) $(EXAMPLE)

$(LIB): $(OBJS)
	$(AR) rcs $@ $^

$(EXAMPLE): example.o $(LIB)
	$(CC) $(CFLAGS) $^ -o $@ $(LDLIBS)

%.o: %.c cib_aes.h
	$(CC) $(CFLAGS) $(INC) -c $< -o $@

clean:
	rm -f $(OBJS) example.o $(LIB) $(EXAMPLE)
