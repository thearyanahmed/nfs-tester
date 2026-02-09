#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <base-url>"
  echo "example: $0 https://nfs-tester-buildpack-e2xke.onstagingocean.app"
  exit 1
fi

BASE_URL="${1%/}"
ENDPOINT="${BASE_URL}/api/v1/test-suite"

echo "requesting ${ENDPOINT} ..."

HTTP_CODE=$(curl -s -o /tmp/nfs-test-result.json -w "%{http_code}" "${ENDPOINT}")

if [ "${HTTP_CODE}" != "200" ]; then
  echo "FAIL: HTTP ${HTTP_CODE}"
  cat /tmp/nfs-test-result.json
  exit 1
fi

jq -r '
  def pad(n): tostring | if length < n then . + (" " * (n - length)) else . end;
  def rpad(n): tostring | if length < n then . + (" " * (n - length)) else .[:n] end;

  def print_table(mode):
    mode as $m |
    .[$m] as $suite |
    $suite.tests | to_entries | map({
      idx: (.key + 1),
      name: .value.name,
      pass: (if .value.pass then "PASS" else "FAIL" end),
      dur:  .value.duration,
      ctx:  (.value.context // ""),
      before: (.value.before // ""),
      after:  (.value.after // "")
    }) |

    # column widths
    ([ .[] | .name  | length ] | max) as $nw |
    ([ .[] | .dur   | length ] | max) as $dw |
    ([ .[] | .ctx   | length ] | max // 0) as $cw |
    ([ .[] | .before | length ] | max // 0) as $bw |
    ([ .[] | .after  | length ] | max // 0) as $aw |

    # clamp to reasonable widths
    (if $cw > 45 then 45 else (if $cw < 7 then 7 else $cw end) end) as $cw |
    (if $bw > 50 then 50 else (if $bw < 6 then 6 else $bw end) end) as $bw |
    (if $aw > 50 then 50 else (if $aw < 5 then 5 else $aw end) end) as $aw |

    # header
    ("  #  | " + ("Test" | rpad($nw)) + " | Pass | " + ("Duration" | rpad($dw)) + " | " + ("Context" | rpad($cw)) + " | " + ("Before" | rpad($bw)) + " | " + ("After" | rpad($aw))),
    ("-----+-" + ("-" * $nw) + "-+------+-" + ("-" * $dw) + "-+-" + ("-" * $cw) + "-+-" + ("-" * $bw) + "-+-" + ("-" * $aw)),

    # rows
    (.[] |
      " " + (if .idx < 10 then " " else "" end) + (.idx | tostring) + " " +
      "| " + (.name | rpad($nw)) +
      " | " + (.pass | rpad(4)) +
      " | " + (.dur | rpad($dw)) +
      " | " + ((.ctx[:$cw]) | rpad($cw)) +
      " | " + ((.before[:$bw]) | rpad($bw)) +
      " | " + ((.after[:$aw]) | rpad($aw))
    );

  "\n\u001b[1m=== NFS Test Suite ===\u001b[0m",
  "user: \(.user)  uid: \(.uid)  gid: \(.gid)",
  "mount: \(.mount_path)",
  "run:   \(.run_id)",
  "time:  \(.timestamp)",
  "",

  "\u001b[1m--- ISOLATED [\(.isolated.summary.pass)/\(.isolated.summary.total)] duration=\(.isolated.duration) ---\u001b[0m",
  "",
  print_table("isolated"),
  "",

  "\u001b[1m--- SHARED [\(.shared.summary.pass)/\(.shared.summary.total)] duration=\(.shared.duration) ---\u001b[0m",
  "",
  print_table("shared"),
  "",

  (if .shared.before.files and (.shared.before.files | length) > 0 then
    "shared dir before: \(.shared.before.files | join(", "))"
  else
    "shared dir before: (empty)"
  end),
  (if .shared.after.files and (.shared.after.files | length) > 0 then
    "shared dir after:  \(.shared.after.files | join(", "))"
  else
    "shared dir after:  (empty)"
  end),
  "",

  (if .overall_summary.fail == 0 then
    "\u001b[32m=== OVERALL: \(.overall_summary.pass)/\(.overall_summary.total) PASS ===\u001b[0m"
  else
    "\u001b[31m=== OVERALL: \(.overall_summary.pass)/\(.overall_summary.total) (\(.overall_summary.fail) FAILED) ===\u001b[0m"
  end)
' /tmp/nfs-test-result.json
