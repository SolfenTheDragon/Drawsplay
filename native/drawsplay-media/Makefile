CC ?= cc
BUILD := build
TARGET := $(BUILD)/drawsplay-media
SRCS := main.c x11_capture.c va_encode.c tile.c
OBJS := $(SRCS:%.c=$(BUILD)/%.o)

CFLAGS ?= -O2
CFLAGS += -std=gnu11 -Wall -Wextra -fno-strict-aliasing -pthread

# Normal form; when the unversioned dev symlinks are missing use LIBS_VERSIONED:
#   make CPPFLAGS_EXTRA=-I/path/to/include LIBS="$(make -s print-libs-versioned)"
LIBS ?= -lva -lva-drm -lX11 -lXext -lXdamage -lXfixes -lz
LIBS_VERSIONED := -l:libva.so.2 -l:libva-drm.so.2 -l:libX11.so.6 -l:libXext.so.6 -l:libXdamage.so.1 -l:libXfixes.so.3 -l:libz.so.1

CPPFLAGS_EXTRA ?=
LDFLAGS_EXTRA ?=

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(CFLAGS) $(LDFLAGS) $(LDFLAGS_EXTRA) -o $@ $(OBJS) $(LIBS) -lpthread

$(BUILD)/%.o: %.c $(wildcard *.h) | $(BUILD)
	$(CC) $(CPPFLAGS) $(CPPFLAGS_EXTRA) $(CFLAGS) -c -o $@ $<

$(BUILD)/va_encode.o: CFLAGS += -O3

$(BUILD):
	mkdir -p $(BUILD)

print-libs-versioned:
	@echo $(LIBS_VERSIONED)

clean:
	rm -rf $(BUILD)

.PHONY: all clean print-libs-versioned
