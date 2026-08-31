# Self-contained build environment for the byte-exact kitsune 1.9.2 rebuild.
#
# Reproduces the 2016 Travis CI compiler: TI ARM Code Generation Tools (CGT)
# 5.1.5, driven by GNU make, on a 32-bit Debian userland. The 32-bit target is
# kept because the reference build ran 32-bit and this keeps the toolchain and
# its output on the exact platform it was captured on (and it runs cleanly under
# qemu-i386 on Apple Silicon).
#
# We only vendor the COMPILER (cgt-5.1.5.tar.gz, ~55 MB). The original build used
# TI Code Composer Studio (Eclipse), but this reproducer drives the
# CCS-generated makefiles with plain `make` instead, so the ~300 MB CCS/Eclipse
# tree is not needed: the container's GNU Make 4.3 is byte-identical to the one
# CCS bundled, and the compiler is referenced through a symlink recreated below.
FROM --platform=linux/386 debian:bullseye

# make: runs the CCS-generated makefiles (GNU Make 4.3, same as CCS bundled).
# python3: section-concatenation .bin extractor. ca-certificates: git over https.
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      make python3 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# CGT ARM 5.1.5 compiler -> /opt/cgt-5.1.5/arm_5.1.5
ADD toolchain/cgt-5.1.5.tar.gz /opt/

# The vendored makefiles hardcode the compiler under a CCS layout
# (/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5). Recreate just that symlink so
# they resolve to the standalone compiler -- no CCS/Eclipse install required.
RUN mkdir -p /root/ti/ccsv6/tools/compiler && \
    ln -s /opt/cgt-5.1.5/arm_5.1.5 /root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5

COPY build_in_container.sh     /root/build_in_container.sh
COPY relink_reforder.sh        /root/relink_reforder.sh
COPY extract_bin.py            /root/extract_bin.py
COPY generated_makefiles.tar.gz /root/generated_makefiles.tar.gz
RUN chmod +x /root/build_in_container.sh /root/relink_reforder.sh

# Source is bind-mounted at /src at run time.
WORKDIR /src
CMD ["sleep", "infinity"]
