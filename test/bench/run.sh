#!/usr/bin/env bash
# kest's benchmark against RoadRunner (docs/benchmarks.md). From the
# repository root, after `make build` and `make php-deps`:
#
#   RR=/path/to/rr test/bench/run.sh
#
# Each target serves the examples/php-hello worker with BENCH_WORKERS workers
# and runs alone. Every scenario runs REPEAT times; the table shows the run
# with the median throughput, and the server's RSS after the scenarios. First,
# test/bench/codec.php shows what each codec costs PHP per request. To compare
# with and without ext-protobuf, point PHPRC at a php.ini that doesn't load it.
set -euo pipefail

: "${RR:?set RR to the rr binary}"
DURATION=${DURATION:-15s}
WARMUP=${WARMUP:-3s}
REPEAT=${REPEAT:-3}
export BENCH_WORKERS=${BENCH_WORKERS:-4}
CLIENTS=${CLIENTS:-"16 64"}

root=$(pwd)
loadgen=$root/bin/loadgen
go build -o "$loadgen" ./test/bench/loadgen

wait_ready() {
	for _ in $(seq 1 100); do
		curl -sf -o /dev/null "$1" && return 0
		sleep 0.1
	done
	echo "not ready: $1" >&2
	return 1
}

# median_line prints the line of the median "rps" among the lines on stdin.
median_line() {
	awk -F'"rps":' '{split($2, a, ","); print a[1] "\t" $0}' | sort -n | awk -F'\t' '{l[NR]=$2} END {print l[int((NR+1)/2)]}'
}

field() {
	sed -E "s/.*\"$1\":([0-9.]+).*/\1/"
}

echo "<!-- $(php -r 'printf("PHP %s, ext-protobuf %s", PHP_VERSION, phpversion("protobuf") ?: "off");') -->"
for codec in json proto; do
	echo "<!-- codec $(php test/bench/codec.php "$codec") -->"
done
printf '| Target | Scenario | Clients | Requests/s | p50 | p99 | Errors |\n|---|---|---|---|---|---|---|\n'
for target in kest-json kest-proto rr; do
	case $target in
	kest-*)
		BENCH_CODEC=${target#kest-} ./bin/kest serve -c test/bench/kest.yaml >/dev/null 2>&1 &
		port=18080
		;;
	rr)
		"$RR" serve -c "$root/test/bench/rr.yaml" -w "$root/examples/php-hello" >/dev/null 2>&1 &
		port=18081
		;;
	esac
	pid=$!
	wait_ready "http://127.0.0.1:$port/"

	for scenario in hello form body-1MB; do
		case $scenario in
		hello) args=(-url "http://127.0.0.1:$port/?name=bench") ;;
		form) args=(-url "http://127.0.0.1:$port/info" -body 'a=1&b[]=2&b[]=3&user[name]=kest') ;;
		body-1MB) args=(-url "http://127.0.0.1:$port/info" -body-bytes 1048576) ;;
		esac
		for c in $CLIENTS; do
			line=$(for _ in $(seq 1 "$REPEAT"); do "$loadgen" "${args[@]}" -c "$c" -d "$DURATION" -warmup "$WARMUP"; done | median_line)
			printf '| %s | %s | %s | %.0f | %s µs | %s µs | %s |\n' "$target" "$scenario" "$c" \
				"$(echo "$line" | field rps)" "$(echo "$line" | field p50_us)" "$(echo "$line" | field p99_us)" \
				"$(echo "$line" | field errors)"
		done
	done
	rss=$(ps -o rss= -p "$pid" | tr -d ' ')
	echo "<!-- $target: server RSS after the scenarios: $((rss / 1024)) MB -->"
	kill "$pid"
	wait "$pid" 2>/dev/null || true
done
