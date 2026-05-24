# Hazy Flow C SDK build.
#
#   make            # builds libhazyflow.a
#   make examples   # builds examples/echo
#   make clean

CC      ?= cc
AR      ?= ar
CFLAGS  ?= -O2 -Wall -Wextra -Werror -std=c11 -fPIC -D_POSIX_C_SOURCE=200809L
INCLUDE := -Iinclude -Isrc

LIB_SRC := src/module.c src/json.c
LIB_OBJ := $(LIB_SRC:.c=.o)

.PHONY: all examples clean
all: libhazyflow.a

libhazyflow.a: $(LIB_OBJ)
	$(AR) rcs $@ $^

src/%.o: src/%.c
	$(CC) $(CFLAGS) $(INCLUDE) -c -o $@ $<

examples: examples/echo

examples/echo: examples/echo.c libhazyflow.a
	$(CC) $(CFLAGS) $(INCLUDE) -o $@ $< libhazyflow.a

clean:
	rm -f $(LIB_OBJ) libhazyflow.a examples/echo
