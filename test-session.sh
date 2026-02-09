#!/bin/bash
# test shared NFS sessions across multiple app instances.
# usage: ./test-session.sh <base-url>
# example: ./test-session.sh https://nfs-tester-oootg.onstagingocean.app

set -euo pipefail

BASE_URL="${1:?usage: $0 <base-url>}"
COOKIE_JAR=$(mktemp)
trap "rm -f $COOKIE_JAR" EXIT

pass=0
fail=0

check() {
  local desc="$1" expected_status="$2" actual_status="$3"
  if [ "$actual_status" -eq "$expected_status" ]; then
    echo "  PASS: $desc (HTTP $actual_status)"
    ((pass++))
  else
    echo "  FAIL: $desc (expected $expected_status, got $actual_status)"
    ((fail++))
  fi
}

echo "=== NFS shared session test ==="
echo "target: $BASE_URL"
echo ""

# 1. login
echo "--- step 1: login as alice ---"
status=$(curl -s -o /dev/null -w '%{http_code}' \
  -c "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"password123"}' \
  "$BASE_URL/api/v1/login")
check "POST /api/v1/login" 200 "$status"

# 2. loop 20x, collect served_by instances
echo ""
echo "--- step 2: loop 20x GET /api/v1/me ---"
declare -A instance_hits 2>/dev/null || true
instances=""
ok_count=0
fail_count=0

for i in $(seq 1 20); do
  resp=$(curl -s -b "$COOKIE_JAR" "$BASE_URL/api/v1/me")
  http_status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" "$BASE_URL/api/v1/me")

  if [ "$http_status" -eq 200 ]; then
    served_by=$(echo "$resp" | grep -o '"served_by"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*: *"//;s/".*//')
    echo "  #$i 200 served_by=$served_by"
    instances="$instances $served_by"
    ((ok_count++))
  else
    echo "  #$i $http_status FAIL"
    ((fail_count++))
  fi
done

# count unique instances
unique=$(echo "$instances" | tr ' ' '\n' | sort -u | grep -v '^$' | wc -l | tr -d ' ')
echo ""
echo "  total: 20, ok: $ok_count, fail: $fail_count"
echo "  unique instances: $unique"

if [ "$ok_count" -eq 20 ]; then
  echo "  PASS: all requests returned 200"
  ((pass++))
else
  echo "  FAIL: $fail_count requests failed"
  ((fail++))
fi

if [ "$unique" -ge 2 ]; then
  echo "  PASS: requests served by $unique different instances"
  ((pass++))
else
  echo "  WARN: all requests hit same instance (check instance_count)"
fi

# 3. list sessions
echo ""
echo "--- step 3: list sessions ---"
status=$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/sessions")
check "GET /api/v1/sessions" 200 "$status"

# 4. logout
echo ""
echo "--- step 4: logout ---"
status=$(curl -s -o /dev/null -w '%{http_code}' \
  -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -X POST \
  "$BASE_URL/api/v1/logout")
check "POST /api/v1/logout" 200 "$status"

# 5. verify session is gone
echo ""
echo "--- step 5: verify session expired ---"
status=$(curl -s -o /dev/null -w '%{http_code}' \
  -b "$COOKIE_JAR" \
  "$BASE_URL/api/v1/me")
check "GET /api/v1/me after logout" 403 "$status"

echo ""
echo "=== results: $pass passed, $fail failed ==="
if [ "$fail" -gt 0 ]; then
  exit 1
fi
