#!/bin/bash
# Relink the freshly built kitsune objects in the REFERENCE's module order:
# subdirectories first (common, debugutils, hashtable, nanopb, protobuf, tests,
# tinyhttp), then root files reverse-alphabetical; archives re-ordered
# reverse-alphabetically to match the reference's library pull order.
set -e
CG=/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5
AR=$CG/bin/armar

rearchive(){ # $1=archive path
  local a="$1" d
  d=$(mktemp -d)
  cp "$a" "$a.alpha"           # keep the alphabetical original
  (cd "$d" && $AR x "$a" >/dev/null)
  local rev
  rev=$(ls "$d" | sort -r)
  rm -f "$a"
  (cd "$d" && $AR r "$a" $rev >/dev/null)
  echo "rearchived $a: $(echo $rev | cut -d' ' -f1-3) ..."
  rm -rf "$d"
}

rearchive /src/oslib/ccs/exe/FreeRTOS.a
rearchive /src/driverlib/ccs/Release/driverlib.a
rearchive /src/simplelink/ccs/exe/simplelink.a
# fatfs.a has one member; leave it

cd /src/kitsune/main/ccs/Release

"$CG/bin/armcl" -mv7M4 --code_state=16 --float_support=FPv4SPD16 --abi=eabi -me -O4 --opt_for_speed=0 --fp_mode=strict -g --no_inlining --gcc --define=ccs --define=ARM --define=TARGET_IS_CC3200 --define=USE_FREERTOS --define=TI_CODEC --display_error_number --diag_warning=225 --diag_wrap=off --gen_func_subsections=on --unaligned_access=off -mt -z -m"kitsune_reord.map" --heap_size=0x200 --stack_size=0x600 -i"$CG/lib" -i"$CG/include" --reread_libs --warn_sections --display_error_number --diag_wrap=off --xml_link_info="kitsune_reord_linkInfo.xml" --rom_model --trampolines=on --unused_section_elimination=on --compress_dwarf=on -o "/src/kitsune/main/ccs/exe/kitsune_reord.out" \
 "./common/wdt_if.obj" "./common/udma_if.obj" "./common/timer_if.obj" "./common/startup_ccs.obj" "./common/i2c_if.obj" "./common/gpio_if.obj" "./common/button_if.obj" \
 "./debugutils/matmessageutils.obj" \
 "./hashtable/swap_mem.obj" "./hashtable/sparse_table.obj" "./hashtable/sparse_multi_table.obj" "./hashtable/running_statistics.obj" "./hashtable/hash_table.obj" "./hashtable/hash_functions.obj" "./hashtable/dump_xsum.obj" "./hashtable/circular_buffer.obj" "./hashtable/bit_array.obj" \
 "./nanopb/pb_encode.obj" "./nanopb/pb_decode.obj" "./nanopb/pb_common.obj" \
 "./protobuf/sync_response.pb.obj" "./protobuf/state.pb.obj" "./protobuf/provision.pb.obj" "./protobuf/periodic.pb.obj" "./protobuf/ntp.pb.obj" "./protobuf/morpheus_ble.pb.obj" "./protobuf/messeji.pb.obj" "./protobuf/matrix.pb.obj" "./protobuf/log.pb.obj" "./protobuf/filetransfer.pb.obj" "./protobuf/file_manifest.pb.obj" "./protobuf/audio_data.pb.obj" "./protobuf/audio_control.pb.obj" "./protobuf/audio_commands.pb.obj" \
 "./tests/TestNetwork.obj" \
 "./tinyhttp/http.obj" "./tinyhttp/header.obj" "./tinyhttp/chunk.obj" \
 "./wifi_cmd.obj" "./ustdlib.obj" "./uartstdio.obj" "./uart_logger.obj" "./top_hci.obj" "./top_board.obj" "./sys_time.obj" "./spi_cmd.obj" "./slip_packet.obj" "./sl_sync.obj" "./sha1.obj" "./rsa.obj" "./rc4.obj" "./rawaudiostatemachine.obj" "./prox_signal.obj" "./proto_utils.obj" "./pinmux.obj" "./pill_settings.obj" "./pcm_handler.obj" "./octogram.obj" "./networktask.obj" "./mcasp_if.obj" "./main.obj" "./long_poll.obj" "./led_cmd.obj" "./led_animations.obj" "./i2c_cmd.obj" "./hw_ver.obj" "./hlo_proto_tools.obj" "./hlo_net_tools.obj" "./hlo_async.obj" "./hellomath.obj" "./hellofilesystem.obj" "./gesture.obj" "./fs_utils.obj" "./fileuploadertask.obj" "./filedownloadmanager.obj" "./fft.obj" "./fault.obj" "./fatfs_cmd.obj" "./dust_cmd.obj" "./diskio.obj" "./crypto_misc.obj" "./commands.obj" "./cmdline.obj" "./circ_buff.obj" "./ble_proto.obj" "./ble_cmd.obj" "./bigint.obj" "./audiotask.obj" "./audioprocessingtask.obj" "./audiohmm.obj" "./audiohelper.obj" "./audiofeatures.obj" "./audiocontrolhelper.obj" "./audioclassifier.obj" "./aes.obj" \
 "../cc3200v1p32.cmd" \
 -l"/src/kitsune/main/ccs/../../../third_party/fatfs/ccs/Release/fatfs.a" -l"/src/kitsune/main/ccs/../../../oslib/ccs/exe/FreeRTOS.a" -l"/src/kitsune/main/ccs/../../../simplelink/ccs/exe/simplelink.a" -l"/src/kitsune/main/ccs/../../../driverlib/ccs/Release/driverlib.a"

echo "== linked =="
ls -la /src/kitsune/main/ccs/exe/kitsune_reord.out
