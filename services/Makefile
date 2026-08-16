# Hello Sense local server + CC3200 tooling
#
# Typical flow:
#   make setup                              # venv, deps, cc3200tool, generate pb2
#   make read-key   PORT=/dev/tty.usbserial-XXXX   # (bootloader mode) recover AES key
#   echo <32-hex-key> > aes.key             # paste the recovered key
#   make gen-certs                          # local CA + *.hello.is server cert (valid from 1950)
#   make write-ca   PORT=/dev/tty.usbserial-XXXX   # (bootloader mode) install our CA
#   make dns        REDIRECT_IP=192.168.x.y # terminal 1 (point the Sense's DNS here)
#   make run                                # terminal 2 (HTTP :80 + HTTPS :443)

PORT        ?= /dev/tty.usbserial-XXXXXXXX
REDIRECT_IP ?=
PY          := ./venv/bin/python
PIP         := ./venv/bin/pip
CC3200      := ./venv/bin/cc3200tool
CC3200_ARGS := -p $(PORT) --reset none --sop2 '~rts'

.PHONY: setup gen-certs dns run backup read-key write-ca restore-ca clean help

## one-time: create venv, install deps + cc3200tool, generate *_pb2.py from proto/
setup:
	python3 -m venv venv
	$(PIP) install --upgrade pip
	$(PIP) install -r requirements.txt
	$(PIP) install git+https://github.com/toniebox-reverse-engineering/cc3200tool.git
	$(PY) -m grpc_tools.protoc -I proto --python_out=. proto/ntp.proto proto/periodic.proto proto/keyword_stats.proto
	@echo "Setup complete."

## generate local CA + server cert (writes ca.* and server.*)
gen-certs:
	$(PY) gen_certs.py

## run the DNS redirector on :53 (sudo). Pass REDIRECT_IP=<your LAN IP>.
dns:
	sudo $(PY) dns_server.py $(REDIRECT_IP)

## run the TLS front-end: HTTP :80 + HTTPS :443 (sudo). Proxies to the backend
## stack in ../../infrastructure/docker, which must already be up.
run:
	sudo $(PY) sense_server.py

## same, but answer time sync in Python instead of proxying to hello-time
## (for running with no backend). Reads the AES key from ./aes.key
run-standalone:
	sudo SENSE_TIME_MODE=local $(PY) sense_server.py

## --- CC3200 flash tools: device must be in BOOTLOADER mode (ID->RTS, power-cycled) ---

## back up the whole device filesystem to ./sense_fs_backup
backup:
	$(CC3200) $(CC3200_ARGS) read_all_files ./sense_fs_backup

## recover the device AES key -> ./key.aes  (then: xxd -p key.aes | tr -d '\n' > aes.key)
read-key:
	$(CC3200) $(CC3200_ARGS) read_file /cert/key.aes ./key.aes

## install our CA as the device's trusted /cert/ca.der
write-ca:
	$(CC3200) $(CC3200_ARGS) write_file ca.der /cert/ca.der

## restore the device's original CA from a backup (./sense_fs_backup/cert/ca.der)
restore-ca:
	$(CC3200) $(CC3200_ARGS) write_file sense_fs_backup/cert/ca.der /cert/ca.der

## remove venv, generated code, certs, logs, data
clean:
	rm -rf venv __pycache__ *_pb2.py ca.key ca.crt ca.der server.key server.crt \
	       server.log sense_data.jsonl key.aes

help:
	@grep -B1 -E '^[a-z-]+:' Makefile | grep -E '^##' | sed 's/## //'
