#!/usr/bin/env bash
set -uo pipefail

# NFS test suite — bash implementation of the 28 core ops from suite.go.
# runs on a droplet (no Go required). designed to test VAST user impersonation
# with different uid/gid combinations.
#
# usage:
#   ./nfs-test-local.sh /mnt/nfs
#   sudo -u testuser1000 ./nfs-test-local.sh /mnt/nfs
#   sudo -u testuser1234 ./nfs-test-local.sh /mnt/nfs

if [ $# -lt 1 ]; then
  echo "usage: $0 <nfs-mount-path>"
  echo "example: $0 /mnt/nfs"
  exit 1
fi

NFS_PATH="${1%/}"
RUN_ID="$$-$(date +%s)"
TEST_DIR="${NFS_PATH}/test-isolated-${RUN_ID}"

PASS=0
FAIL=0
TOTAL=0
RESULTS=()

color_green="\033[32m"
color_red="\033[31m"
color_bold="\033[1m"
color_reset="\033[0m"

run_test() {
  local name="$1"
  local context="$2"
  shift 2

  TOTAL=$((TOTAL + 1))
  local before=""
  local after=""
  local details=""
  local start_ns
  start_ns=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')

  # globals set by each test function
  TEST_BEFORE=""
  TEST_AFTER=""
  TEST_DETAILS=""

  # run in current shell so globals propagate back, capture stderr
  local tmp_err
  tmp_err=$(mktemp)
  "$@" 2>"$tmp_err"
  local rc=$?
  local output
  output=$(cat "$tmp_err")
  rm -f "$tmp_err"

  local end_ns
  end_ns=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')
  local dur_ms=$(( (end_ns - start_ns) / 1000000 ))

  before="$TEST_BEFORE"
  after="$TEST_AFTER"
  details="$TEST_DETAILS"

  local status
  if [ $rc -eq 0 ]; then
    PASS=$((PASS + 1))
    status="PASS"
  else
    FAIL=$((FAIL + 1))
    status="FAIL"
    if [ -n "$output" ]; then
      details="ERROR: ${output}"
    fi
  fi

  RESULTS+=("$(printf "%s\t%s\t%s\t%dms\t%s\t%s\t%s" "$name" "$status" "$context" "$dur_ms" "$before" "$after" "$details")")
}

# --- test functions ---
# each sets TEST_BEFORE, TEST_AFTER, TEST_DETAILS and returns 0/1

test_create_file() {
  mkdir -p "$TEST_DIR"
  local f="${TEST_DIR}/test.txt"
  TEST_BEFORE="test.txt exists=false"
  echo -n "hello nfs" > "$f"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  TEST_AFTER="test.txt size=${sz}"
  TEST_DETAILS="created test.txt (${sz} bytes)"
}

test_read_file() {
  local f="${TEST_DIR}/test.txt"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  TEST_BEFORE="test.txt size=${sz}"
  local content
  content=$(cat "$f")
  if [ "$content" != "hello nfs" ]; then
    TEST_AFTER="content mismatch: got '${content}'"
    return 1
  fi
  TEST_AFTER="read ${#content} bytes, match=true"
  TEST_DETAILS="content verified"
}

test_stat_file() {
  local f="${TEST_DIR}/test.txt"
  local mode
  mode=$(stat -c%A "$f" 2>/dev/null || stat -f%Sp "$f")
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  TEST_DETAILS="mode=${mode} size=${sz}"
}

test_append_file() {
  local f="${TEST_DIR}/test.txt"
  local before_content
  before_content=$(cat "$f")
  TEST_BEFORE="size=${#before_content} content='${before_content}'"
  printf "\nappended line" >> "$f"
  local after_content
  after_content=$(cat "$f")
  TEST_AFTER="size=${#after_content}"
  TEST_DETAILS="appended 14 bytes"
}

test_overwrite_file() {
  local f="${TEST_DIR}/test.txt"
  local before_sz
  before_sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  TEST_BEFORE="size=${before_sz}"
  echo -n "overwritten content" > "$f"
  local after_content
  after_content=$(cat "$f")
  if [ "$after_content" != "overwritten content" ]; then
    return 1
  fi
  TEST_AFTER="content='${after_content}'"
  TEST_DETAILS="overwritten and verified"
}

test_chmod_file() {
  local f="${TEST_DIR}/test.txt"
  local before_mode
  before_mode=$(stat -c%A "$f" 2>/dev/null || stat -f%Sp "$f")
  TEST_BEFORE="mode=${before_mode}"
  chmod 755 "$f"
  local after_mode
  after_mode=$(stat -c%A "$f" 2>/dev/null || stat -f%Sp "$f")
  TEST_AFTER="mode=${after_mode}"
  TEST_DETAILS="chmod ${before_mode} -> ${after_mode}"
}

test_rename_file() {
  local src="${TEST_DIR}/test.txt"
  local dst="${TEST_DIR}/renamed.txt"
  TEST_BEFORE="test.txt exists=true, renamed.txt exists=false"
  mv "$src" "$dst"
  if [ ! -f "$dst" ]; then return 1; fi
  TEST_AFTER="test.txt exists=false, renamed.txt exists=true"
  mv "$dst" "$src"
  TEST_DETAILS="renamed and renamed back"
}

test_copy_file() {
  local src="${TEST_DIR}/test.txt"
  local dst="${TEST_DIR}/test-copy.txt"
  TEST_BEFORE="test-copy.txt exists=false"
  cp "$src" "$dst"
  local sz
  sz=$(stat -c%s "$dst" 2>/dev/null || stat -f%z "$dst")
  TEST_AFTER="test-copy.txt exists=true size=${sz}"
  TEST_DETAILS="copied ${sz} bytes"
}

test_symlink() {
  local target="${TEST_DIR}/test.txt"
  local link="${TEST_DIR}/test-link.txt"
  TEST_BEFORE="test-link.txt exists=false"
  ln -s "$target" "$link"
  local resolved
  resolved=$(readlink "$link")
  TEST_AFTER="test-link.txt -> $(basename "$resolved")"
  TEST_DETAILS="symlink created and resolved"
}

test_mkdir() {
  local d="${TEST_DIR}/subdir"
  TEST_BEFORE="subdir/ exists=false"
  mkdir "$d"
  if [ ! -d "$d" ]; then return 1; fi
  TEST_AFTER="subdir/ exists=true"
  TEST_DETAILS="created subdir/"
}

test_nested_mkdir() {
  local d="${TEST_DIR}/deep/nested/dir"
  TEST_BEFORE="deep/ exists=false"
  mkdir -p "$d"
  if [ ! -d "$d" ]; then return 1; fi
  TEST_AFTER="deep/nested/dir/ exists=true"
  TEST_DETAILS="created deep/nested/dir/"
}

test_create_in_subdir() {
  local f="${TEST_DIR}/subdir/subfile.txt"
  TEST_BEFORE="subdir/subfile.txt exists=false"
  echo -n "subdir content" > "$f"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  TEST_AFTER="subdir/subfile.txt size=${sz}"
  TEST_DETAILS="created subdir/subfile.txt"
}

test_cross_dir_rename() {
  local src="${TEST_DIR}/subdir/subfile.txt"
  local dst="${TEST_DIR}/deep/moved.txt"
  TEST_BEFORE="subdir/subfile.txt -> (exists)"
  mv "$src" "$dst"
  if [ ! -f "$dst" ]; then return 1; fi
  TEST_AFTER="deep/moved.txt -> (exists)"
  TEST_DETAILS="moved subdir/subfile.txt -> deep/moved.txt"
}

test_delete_file() {
  TEST_BEFORE="exist: test-copy.txt, test-link.txt"
  rm -f "${TEST_DIR}/test-copy.txt" "${TEST_DIR}/test-link.txt"
  local remaining=""
  [ -f "${TEST_DIR}/test-copy.txt" ] && remaining="test-copy.txt "
  [ -f "${TEST_DIR}/test-link.txt" ] && remaining="${remaining}test-link.txt"
  if [ -n "$remaining" ]; then
    TEST_AFTER="exist: ${remaining}"
    return 1
  fi
  TEST_AFTER="exist: (none)"
  TEST_DETAILS="deleted test-copy.txt and test-link.txt"
}

test_rmdir() {
  TEST_BEFORE="dirs: deep/, subdir/"
  rm -rf "${TEST_DIR}/deep" "${TEST_DIR}/subdir"
  local remaining=""
  [ -d "${TEST_DIR}/deep" ] && remaining="deep/ "
  [ -d "${TEST_DIR}/subdir" ] && remaining="${remaining}subdir/"
  if [ -n "$remaining" ]; then
    TEST_AFTER="dirs: ${remaining}"
    return 1
  fi
  TEST_AFTER="dirs: (none)"
  TEST_DETAILS="removed subdir/ and deep/"
}

test_large_file_1mb() {
  local f="${TEST_DIR}/large.bin"
  TEST_BEFORE="large.bin exists=false"
  local start_t
  start_t=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')
  dd if=/dev/urandom of="$f" bs=1048576 count=1 2>/dev/null
  local end_t
  end_t=$(date +%s%N 2>/dev/null || python3 -c 'import time; print(int(time.time()*1e9))')
  local dur_ms=$(( (end_t - start_t) / 1000000 ))
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  if [ "$sz" -ne 1048576 ]; then
    TEST_AFTER="large.bin size=${sz} (expected 1048576)"
    return 1
  fi
  # read back
  local read_sz
  read_sz=$(cat "$f" | wc -c | tr -d ' ')
  rm -f "$f"
  if [ "$read_sz" -ne 1048576 ]; then return 1; fi
  local speed="N/A"
  if [ "$dur_ms" -gt 0 ]; then
    speed=$(awk "BEGIN {printf \"%.2f\", 1048576.0 / ($dur_ms / 1000.0) / 1048576.0}")
  fi
  TEST_AFTER="large.bin size=1048576 verified=true"
  TEST_DETAILS="${dur_ms}ms, ${speed} MB/s"
}

test_concurrent_writes() {
  local pids=()
  local ok=0
  for i in $(seq 0 4); do
    (echo -n "writer ${i}" > "${TEST_DIR}/concurrent-${i}.txt") &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do
    wait "$pid" || ok=1
  done
  if [ $ok -ne 0 ]; then return 1; fi
  # verify
  for i in $(seq 0 4); do
    local content
    content=$(cat "${TEST_DIR}/concurrent-${i}.txt")
    if [ "$content" != "writer ${i}" ]; then
      TEST_DETAILS="writer ${i} mismatch: got '${content}'"
      return 1
    fi
    rm -f "${TEST_DIR}/concurrent-${i}.txt"
  done
  TEST_DETAILS="5 concurrent writes verified"
}

test_file_lock() {
  local f="${TEST_DIR}/locktest.txt"
  echo -n "lock test" > "$f"
  TEST_BEFORE="content='lock test'"
  # overwrite first 6 bytes using dd
  echo -n "locked" | dd of="$f" bs=1 count=6 conv=notrunc 2>/dev/null
  local after_content
  after_content=$(cat "$f")
  TEST_AFTER="content='${after_content}'"
  rm -f "$f"
  TEST_DETAILS="write-at succeeded"
}

test_truncate_file() {
  local f="${TEST_DIR}/truncate-test.txt"
  local original="this is a long string for truncation"
  echo -n "$original" > "$f"
  TEST_BEFORE="size=${#original}"
  truncate -s 10 "$f"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  local content
  content=$(cat "$f")
  rm -f "$f"
  if [ "$sz" -ne 10 ]; then
    TEST_AFTER="size=${sz} (expected 10)"
    return 1
  fi
  TEST_AFTER="size=${sz}"
  TEST_DETAILS="truncated to 10 bytes"
}

test_hardlink() {
  local src="${TEST_DIR}/test.txt"
  local dst="${TEST_DIR}/test-hardlink.txt"
  TEST_BEFORE="test-hardlink.txt exists=false"
  ln "$src" "$dst"
  local src_content
  src_content=$(cat "$src")
  local dst_content
  dst_content=$(cat "$dst")
  rm -f "$dst"
  if [ "$src_content" != "$dst_content" ]; then
    return 1
  fi
  TEST_AFTER="test-hardlink.txt content matches (${#src_content} bytes)"
  TEST_DETAILS="hardlink created, content matches"
}

test_mkfifo() {
  local f="${TEST_DIR}/test.fifo"
  TEST_BEFORE="test.fifo exists=false"
  mkfifo "$f"
  if [ ! -p "$f" ]; then return 1; fi
  local mode
  mode=$(stat -c%A "$f" 2>/dev/null || stat -f%Sp "$f")
  TEST_AFTER="test.fifo mode=${mode}"
  rm -f "$f"
  TEST_DETAILS="fifo created"
}

test_write_binary() {
  local f="${TEST_DIR}/binary.bin"
  TEST_BEFORE="binary.bin exists=false"
  # write all 256 byte values
  python3 -c 'import sys; sys.stdout.buffer.write(bytes(range(256)))' > "$f" 2>/dev/null \
    || printf '%b' "$(for i in $(seq 0 255); do printf '\\x%02x' "$i"; done)" > "$f"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  # read back and verify
  local read_sz
  read_sz=$(cat "$f" | wc -c | tr -d ' ')
  rm -f "$f"
  if [ "$sz" -ne 256 ] || [ "$read_sz" -ne 256 ]; then
    TEST_AFTER="size=${sz} read_back=${read_sz}"
    return 1
  fi
  TEST_AFTER="binary.bin size=256 match=true"
  TEST_DETAILS="256-byte binary round-trip verified"
}

test_mtime_check() {
  local f="${TEST_DIR}/mtime-test.txt"
  echo -n "before" > "$f"
  local mtime1
  mtime1=$(stat -c%Y "$f" 2>/dev/null || stat -f%m "$f")
  TEST_BEFORE="mtime=${mtime1}"
  sleep 1
  echo -n "after modification" > "$f"
  local mtime2
  mtime2=$(stat -c%Y "$f" 2>/dev/null || stat -f%m "$f")
  TEST_AFTER="mtime=${mtime2}"
  rm -f "$f"
  if [ "$mtime2" -le "$mtime1" ]; then
    TEST_DETAILS="mtime did NOT advance"
    return 1
  fi
  TEST_DETAILS="mtime advanced (delta=$((mtime2 - mtime1))s)"
}

test_readdir_many() {
  local d="${TEST_DIR}/readdir-test"
  mkdir -p "$d"
  for i in $(seq -w 0 49); do
    echo -n "file ${i}" > "${d}/file-${i}.txt"
  done
  TEST_BEFORE="readdir-test/ files=50"
  local count
  count=$(ls "$d" | wc -l | tr -d ' ')
  rm -rf "$d"
  if [ "$count" -ne 50 ]; then
    TEST_AFTER="readdir returned ${count} entries"
    return 1
  fi
  TEST_AFTER="readdir returned ${count} entries"
  TEST_DETAILS="created and listed 50 files"
}

test_sparse_write() {
  local f="${TEST_DIR}/sparse.bin"
  TEST_BEFORE="sparse.bin exists=false"
  # create sparse file: seek to 1MB, write 16 bytes
  dd if=/dev/zero of="$f" bs=1 count=0 seek=1048576 2>/dev/null
  echo -n "sparse data here" | dd of="$f" bs=1 seek=1048576 conv=notrunc 2>/dev/null
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  # read back at offset
  local readback
  readback=$(dd if="$f" bs=1 skip=1048576 count=16 2>/dev/null)
  rm -f "$f"
  if [ "$readback" != "sparse data here" ]; then
    TEST_AFTER="readback mismatch: '${readback}'"
    return 1
  fi
  TEST_AFTER="sparse.bin logical_size=${sz}"
  TEST_DETAILS="wrote 16 bytes at offset 1048576"
}

test_temp_file() {
  TEST_BEFORE="target dir=${TEST_DIR} (NFS mount, not /tmp)"
  local f
  f=$(mktemp "${TEST_DIR}/nfs-test-XXXXXX.tmp")
  echo -n "temp file content" > "$f"
  local sz
  sz=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
  local name
  name=$(basename "$f")
  rm -f "$f"
  TEST_AFTER="created ${name} (${sz} bytes)"
  TEST_DETAILS="temp file ${name}: ${sz} bytes"
}

test_exclusive_create() {
  local f="${TEST_DIR}/exclusive.txt"
  rm -f "$f"
  TEST_BEFORE="exclusive.txt exists=false"
  # O_CREAT|O_EXCL via bash set -C (noclobber)
  (set -C; echo -n "exclusive create" > "$f") 2>/dev/null
  if [ ! -f "$f" ]; then return 1; fi
  # second attempt should fail
  local rc=0
  (set -C; echo -n "should fail" > "$f") 2>/dev/null || rc=$?
  rm -f "$f"
  if [ $rc -eq 0 ]; then
    TEST_DETAILS="O_EXCL did NOT reject duplicate"
    return 1
  fi
  TEST_AFTER="exclusive.txt exists=true, 2nd O_EXCL rejected"
  TEST_DETAILS="O_EXCL create succeeded, duplicate correctly rejected"
}

test_seek_read_write() {
  local f="${TEST_DIR}/seektest.txt"
  echo -n "AAAAAAAAAA" > "$f"
  TEST_BEFORE="content='AAAAAAAAAA'"
  echo -n "BBBBB" | dd of="$f" bs=1 seek=5 conv=notrunc 2>/dev/null
  local content
  content=$(cat "$f")
  rm -f "$f"
  if [ "$content" != "AAAAABBBBB" ]; then
    TEST_AFTER="content='${content}'"
    TEST_DETAILS="mismatch: expected 'AAAAABBBBB'"
    return 1
  fi
  TEST_AFTER="content='${content}'"
  TEST_DETAILS="seek write verified: '${content}'"
}

# --- main ---

echo ""
echo -e "${color_bold}=== NFS Local Test Suite ===${color_reset}"
echo "user: $(whoami)  uid=$(id -u)  gid=$(id -g)"
echo "nfs:  ${NFS_PATH}"
echo "dir:  ${TEST_DIR}"
echo "time: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo ""

run_test "create_file"       "os.WriteFile test.txt on NFS"               test_create_file
run_test "read_file"         "os.ReadFile + content verify"               test_read_file
run_test "stat_file"         "os.Stat metadata check"                     test_stat_file
run_test "append_file"       "O_APPEND write"                             test_append_file
run_test "overwrite_file"    "overwrite + read-back verify"               test_overwrite_file
run_test "chmod_file"        "chmod permission change"                    test_chmod_file
run_test "rename_file"       "rename within same dir"                     test_rename_file
run_test "copy_file"         "read src + write dst copy"                  test_copy_file
run_test "symlink"           "ln -s + readlink"                           test_symlink
run_test "mkdir"             "mkdir single dir"                           test_mkdir
run_test "nested_mkdir"      "mkdir -p 3-level deep"                     test_nested_mkdir
run_test "create_in_subdir"  "write file inside subdir"                   test_create_in_subdir
run_test "cross_dir_rename"  "mv across directories"                      test_cross_dir_rename
run_test "delete_file"       "rm file deletion"                           test_delete_file
run_test "rmdir"             "rm -rf recursive"                           test_rmdir
run_test "large_file_1mb"    "1MB write + read-back throughput"           test_large_file_1mb
run_test "concurrent_writes" "5 background writes simultaneously"         test_concurrent_writes
run_test "file_lock"         "dd write-at with conv=notrunc"              test_file_lock
run_test "truncate_file"     "truncate shrink file"                       test_truncate_file
run_test "hardlink"          "ln hard link"                               test_hardlink
run_test "mkfifo"            "mkfifo named pipe"                          test_mkfifo
run_test "write_binary"      "write all 256 byte values, read back"       test_write_binary
run_test "mtime_check"       "verify mtime advances after write"          test_mtime_check
run_test "readdir_many"      "create 50 files + ls count"                 test_readdir_many
run_test "sparse_write"      "seek to 1MB offset, write 16 bytes"         test_sparse_write
run_test "temp_file"         "mktemp on NFS (not /tmp)"                   test_temp_file
run_test "exclusive_create"  "noclobber (O_CREAT|O_EXCL)"                 test_exclusive_create
run_test "seek_read_write"   "dd seek to offset 5, overwrite, verify"     test_seek_read_write

# cleanup
rm -rf "$TEST_DIR"

# --- print results ---

# find max column widths
max_name=4
max_ctx=7
max_before=6
max_after=5
for r in "${RESULTS[@]}"; do
  IFS=$'\t' read -r name status ctx dur before after details <<< "$r"
  [ ${#name} -gt $max_name ] && max_name=${#name}
  [ ${#ctx} -gt $max_ctx ] && max_ctx=${#ctx}
  [ ${#before} -gt $max_before ] && max_before=${#before}
  [ ${#after} -gt $max_after ] && max_after=${#after}
done

# clamp
[ $max_ctx -gt 42 ] && max_ctx=42
[ $max_before -gt 45 ] && max_before=45
[ $max_after -gt 45 ] && max_after=45

# header
sep_name=$(printf '%*s' "$max_name" '' | tr ' ' '-')
sep_ctx=$(printf '%*s' "$max_ctx" '' | tr ' ' '-')
sep_before=$(printf '%*s' "$max_before" '' | tr ' ' '-')
sep_after=$(printf '%*s' "$max_after" '' | tr ' ' '-')

printf "  #  | %-${max_name}s | Pass | %-8s | %-${max_ctx}s | %-${max_before}s | %-${max_after}s\n" \
  "Test" "Duration" "Context" "Before" "After"
printf -- "-----+-%s-+------+----------+-%s-+-%s-+-%s\n" \
  "$sep_name" "$sep_ctx" "$sep_before" "$sep_after"

idx=0
for r in "${RESULTS[@]}"; do
  idx=$((idx + 1))
  IFS=$'\t' read -r name status ctx dur before after details <<< "$r"
  local_color="$color_green"
  [ "$status" = "FAIL" ] && local_color="$color_red"
  printf " %2d | %-${max_name}s | ${local_color}%-4s${color_reset} | %-8s | %-${max_ctx}.${max_ctx}s | %-${max_before}.${max_before}s | %-${max_after}.${max_after}s\n" \
    "$idx" "$name" "$status" "$dur" "$ctx" "$before" "$after"
done

echo ""
if [ $FAIL -eq 0 ]; then
  echo -e "${color_green}=== RESULT: ${PASS}/${TOTAL} PASS ===${color_reset}"
else
  echo -e "${color_red}=== RESULT: ${PASS}/${TOTAL} (${FAIL} FAILED) ===${color_reset}"
fi
echo ""

exit $FAIL
