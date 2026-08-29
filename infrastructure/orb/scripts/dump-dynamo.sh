#!/bin/bash
# Dumps every non-empty DynamoDB table to JSON, one file per table.
#
# The migrator reads these dumps rather than DynamoDB directly, for three
# reasons: the migration then runs against a fixed snapshot instead of a table
# the Orb is still writing to, the dumps double as a backup of the source data,
# and the orb Go module avoids taking a dependency on the AWS SDK for a program
# that runs once.
#
# Output: orb/migrations/dump/<table>.json, in the DynamoDB scan format
# ({"Items": [{"attr": {"S": "value"}}, ...]}).
set -uo pipefail

ENDPOINT=${ENDPOINT:-http://localhost:7777}
OUT=${OUT:-$(cd "$(dirname "$0")/.." && pwd)/migrations/dump}

export AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-test}
export AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-test}
export AWS_DEFAULT_REGION=${AWS_DEFAULT_REGION:-us-east-1}
unset AWS_PROFILE

mkdir -p "$OUT"
echo "dumping to $OUT"

TABLES=$(aws dynamodb list-tables --endpoint-url "$ENDPOINT" --query 'TableNames' --output text 2>/dev/null | tr '\t' '\n')
[ -z "$TABLES" ] && { echo "no tables found at $ENDPOINT" >&2; exit 1; }

total=0
for t in $TABLES; do
  # Skip KCL lease tables: they coordinate Kinesis consumers and Kinesis is
  # being removed, so there is nothing to carry across.
  case "$t" in
    *ConsumerLocal*|*WorkerLocal*|*GeneratorLocal*) continue ;;
  esac

  n=$(aws dynamodb scan --table-name "$t" --endpoint-url "$ENDPOINT" --select COUNT --query 'Count' 2>/dev/null)
  [ "${n:-0}" -eq 0 ] 2>/dev/null && continue

  if aws dynamodb scan --table-name "$t" --endpoint-url "$ENDPOINT" --output json > "$OUT/$t.json" 2>/dev/null; then
    got=$(python3 -c "import json;print(len(json.load(open('$OUT/$t.json'))['Items']))" 2>/dev/null || echo "?")
    printf "  %-28s %6s rows\n" "$t" "$got"
    total=$((total + ${got:-0}))
  else
    printf "  %-28s FAILED\n" "$t"
  fi
done

echo "dumped $total rows"
