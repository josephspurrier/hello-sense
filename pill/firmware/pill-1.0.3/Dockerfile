# nRF51 firmware cross-compile env: exact 2013 ARM GCC in an amd64 container.
FROM --platform=linux/amd64 debian:bullseye

# 2013 ARM GCC ships 32-bit (i686) binaries, so enable i386 multiarch libs.
RUN dpkg --add-architecture i386 && apt-get update && apt-get install -y --no-install-recommends \
      wget bzip2 make git ca-certificates \
      python2.7 python3 python3-pip \
      libc6:i386 libstdc++6:i386 libncurses5:i386 zlib1g:i386 \
 && ln -sf /usr/bin/python2.7 /usr/bin/python \
 && rm -rf /var/lib/apt/lists/*

# Exact toolchain kodobannin pins (GCC 4.7 2013q3).
RUN wget -q https://launchpadlibrarian.net/151487636/gcc-arm-none-eabi-4_7-2013q3-20130916-linux.tar.bz2 \
      -O /tmp/gcc.tar.bz2 \
 && tar xjf /tmp/gcc.tar.bz2 -C /opt \
 && rm /tmp/gcc.tar.bz2

ENV PATH=/opt/gcc-arm-none-eabi-4_7-2013q3/bin:$PATH
WORKDIR /src

# native gcc for host-side build tools (tools/crc16 etc.)
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*

# xxd + openssl for the CRC/HMAC image post-processing
RUN apt-get update && apt-get install -y --no-install-recommends xxd openssl && rm -rf /var/lib/apt/lists/*
