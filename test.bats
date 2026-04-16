#!/usr/bin/env bats

# Combined smoke + integration test suite for docker-port-forward.
#
# Smoke tests (the block marked "CLI smoke tests") run without a Docker
# daemon and cover help, metadata, and flag validation.
#
# Integration tests ("feature integration tests") require a reachable Docker
# daemon and automatically skip when one is not available. They spin up a
# disposable target container (nginx:alpine or redis:alpine) and drive the
# CLI end-to-end, verifying real container state.
#
# Run with:
#   bats test.bats                      # both smoke and integration
#   bats test.bats -f 'smoke'           # to limit by name substring

export SYSTEM_NAME="$(uname -s | tr '[:upper:]' '[:lower:]')"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64) ARCH="arm64" ;;
esac
export DOCKER_PORT_FORWARD="${BATS_TEST_DIRNAME}/build/${SYSTEM_NAME}/docker-port-forward-${ARCH}"
export INTEGRATION_LABEL="dpf-bats-integration"

# ---------------------------------------------------------------------------
# bats helpers
# ---------------------------------------------------------------------------

flunk() {
  {
    if [[ "$#" -eq 0 ]]; then
      cat -
    else
      echo "$*"
    fi
  }
  return 1
}

assert_equal() {
  if [[ "$1" != "$2" ]]; then
    {
      echo "expected: $1"
      echo "actual:   $2"
    } | flunk
  fi
}

assert_exit_status() {
  exit_status="$1"
  if [[ "$status" -ne "$exit_status" ]]; then
    {
      echo "expected exit status: $exit_status"
      echo "actual exit status:   $status"
    } | flunk
  fi
}

assert_failure() {
  if [[ "$status" -eq 0 ]]; then
    echo "output: $output"
    flunk "expected failed exit status"
  elif [[ "$#" -gt 0 ]]; then
    assert_output "$1"
  fi
}

assert_success() {
  if [[ "$status" -ne 0 ]]; then
    echo "output: $output"
    flunk "command failed with exit status $status"
  elif [[ "$#" -gt 0 ]]; then
    assert_output "$1"
  fi
}

assert_output() {
  local expected
  if [[ $# -eq 0 ]]; then
    expected="$(cat -)"
  else
    expected="$1"
  fi
  assert_equal "$expected" "$output"
}

assert_output_contains() {
  if [[ "$output" != *"$1"* ]]; then
    echo "output: $output"
    flunk "expected output to contain: $1"
  fi
}

refute_output_contains() {
  local unexpected="$1"
  if [[ "$output" == *"$unexpected"* ]]; then
    echo "output: $output"
    flunk "expected output NOT to contain: $unexpected"
  fi
}

# require_docker skips the current test when no Docker daemon is reachable.
require_docker() {
  if [ "${DOCKER_AVAILABLE:-0}" != "1" ]; then
    skip "Docker daemon not reachable"
  fi
}

# free_port prints an unused TCP port on 127.0.0.1 by asking the kernel for one.
free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

# wait_http waits up to the given seconds for the URL to return 200 OK.
wait_http() {
  local url="$1"
  local timeout="${2:-10}"
  local deadline=$(($(date +%s) + timeout))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS -m 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

# target_id prints the full id of $TARGET.
target_id() {
  docker inspect -f '{{.Id}}' "$TARGET"
}

# helpers_for_target prints the count of helper containers for the current
# $TARGET filtered by state (e.g., "running", "" for all).
helpers_for_target() {
  local state_filter=()
  if [ -n "${1:-}" ]; then
    state_filter+=("--filter" "status=${1}")
  fi
  docker ps -aq "${state_filter[@]}" \
    --filter "label=com.dokku.port-forward=true" \
    --filter "label=com.dokku.port-forward.target=$(target_id)" \
    | wc -l | tr -d ' '
}

# start_nginx_target starts the standard nginx:alpine target used by most
# integration tests. Exports $TARGET.
start_nginx_target() {
  export TARGET="bats-target-${BATS_TEST_NUMBER}-$$"
  docker rm -f "$TARGET" >/dev/null 2>&1 || true
  docker run -d --name "$TARGET" --label "${INTEGRATION_LABEL}=true" nginx:alpine >/dev/null
}

# start_redis_target starts a redis:alpine target (unprivileged port 6379)
# for the auto-detect test. Exports $TARGET.
start_redis_target() {
  export TARGET="bats-redis-${BATS_TEST_NUMBER}-$$"
  docker rm -f "$TARGET" >/dev/null 2>&1 || true
  docker run -d --name "$TARGET" --label "${INTEGRATION_LABEL}=true" redis:alpine >/dev/null
}

# ---------------------------------------------------------------------------
# setup / teardown
# ---------------------------------------------------------------------------

setup_file() {
  export DOCKER_AVAILABLE=0
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    export DOCKER_AVAILABLE=1
    docker pull nginx:alpine >/dev/null
    docker pull redis:alpine >/dev/null
  fi
}

setup() {
  export TARGET=""
}

teardown() {
  # Remove every helper container this test created + the target (if any).
  if [ "${DOCKER_AVAILABLE:-0}" = "1" ]; then
    docker ps -aq --filter "label=${INTEGRATION_LABEL}=true" | xargs -r docker rm -f >/dev/null 2>&1 || true
    if [ -n "$TARGET" ]; then
      docker rm -f "$TARGET" >/dev/null 2>&1 || true
    fi
  fi
}

# ---------------------------------------------------------------------------
# CLI smoke tests (no Docker daemon required)
# ---------------------------------------------------------------------------

@test "smoke: binary exists" {
  [ -x "$DOCKER_PORT_FORWARD" ]
}

@test "smoke: help prints usage" {
  run "$DOCKER_PORT_FORWARD" --help
  assert_success
  assert_output_contains "port-forward"
  assert_output_contains "Forward one or more local ports to a container"
}

@test "smoke: port-forward --help documents detach, name, and label" {
  run "$DOCKER_PORT_FORWARD" port-forward --help
  assert_success
  assert_output_contains "--address"
  assert_output_contains "--detach"
  assert_output_contains "--helper-image"
  assert_output_contains "--label"
  assert_output_contains "--name"
  assert_output_contains "<target>"
  assert_output_contains "ports..."
}

@test "smoke: docker-cli-plugin-metadata emits valid JSON" {
  run "$DOCKER_PORT_FORWARD" docker-cli-plugin-metadata
  assert_success
  assert_output_contains '"SchemaVersion"'
  assert_output_contains '"Vendor"'
  assert_output_contains '"ShortDescription"'
}

@test "smoke: port-forward with no args errors" {
  run "$DOCKER_PORT_FORWARD" port-forward
  assert_failure
  assert_output_contains "usage: port-forward"
}

@test "smoke: port-forward rejects invalid port spec" {
  run "$DOCKER_PORT_FORWARD" port-forward container/foo bad-port
  assert_failure
  assert_output_contains "invalid remote port"
}

@test "smoke: port-forward rejects unknown protocol suffix" {
  run "$DOCKER_PORT_FORWARD" port-forward container/foo 53:53/sctp
  assert_failure
  assert_output_contains "unsupported protocol"
}

@test "smoke: port-forward rejects invalid --pull value" {
  run "$DOCKER_PORT_FORWARD" port-forward --pull bogus container/foo 8080:80
  assert_failure
  assert_output_contains "invalid --pull value"
}

@test "smoke: port-forward rejects invalid --label format" {
  run "$DOCKER_PORT_FORWARD" port-forward --label badformat container/foo 8080:80
  assert_failure
  assert_output_contains "invalid --label value"
}

@test "smoke: port-forward rejects empty service name" {
  run "$DOCKER_PORT_FORWARD" port-forward service/ 8080:80
  assert_failure
  assert_output_contains "service name is empty"
}

@test "smoke: version prints something" {
  run "$DOCKER_PORT_FORWARD" version
  assert_success
}

@test "smoke: cleanup --help prints flags" {
  run "$DOCKER_PORT_FORWARD" port-forward cleanup --help
  assert_success
  assert_output_contains "--dry-run"
  assert_output_contains "--name"
  assert_output_contains "--target"
  assert_output_contains "Remove leftover port-forward helper containers"
}

# ---------------------------------------------------------------------------
# Feature integration tests (require a Docker daemon)
# ---------------------------------------------------------------------------

@test "integration: detached forward stays up and serves traffic" {
  require_docker
  start_nginx_target
  PORT=$(free_port)
  NAME="dpf-bats-detach-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PORT:80"
  assert_success
  assert_output_contains "Started detached helper \"$NAME\""

  run docker inspect --format '{{.State.Running}}' "$NAME"
  assert_success
  assert_output "true"

  if ! wait_http "http://127.0.0.1:${PORT}/" 10; then
    flunk "curl to forwarded port ${PORT} never succeeded"
  fi
}

@test "integration: extra labels are applied to the helper container" {
  require_docker
  start_nginx_target
  PORT=$(free_port)
  NAME="dpf-bats-labels-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    --label team=backend \
    --label env=dev \
    "container/$TARGET" "$PORT:80"
  assert_success

  run docker inspect --format '{{index .Config.Labels "team"}}' "$NAME"
  assert_success
  assert_output "backend"

  run docker inspect --format '{{index .Config.Labels "env"}}' "$NAME"
  assert_success
  assert_output "dev"

  run docker inspect --format '{{index .Config.Labels "com.dokku.port-forward"}}' "$NAME"
  assert_success
  assert_output "true"
}

@test "integration: invalid --label format is rejected before any helper is created" {
  require_docker
  start_nginx_target
  PORT=$(free_port)

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --label nokey \
    "container/$TARGET" "$PORT:80"
  assert_failure
  assert_output_contains "invalid --label value"

  # No helper should have been created.
  run helpers_for_target
  assert_output "0"
}

@test "integration: multiple ports forwarded through one helper" {
  require_docker
  start_nginx_target
  P1=$(free_port)
  P2=$(free_port)
  NAME="dpf-bats-multi-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$P1:80" "$P2:80"
  assert_success

  # One helper, two reachable bindings.
  run helpers_for_target running
  assert_output "1"

  if ! wait_http "http://127.0.0.1:${P1}/" 10; then flunk "first port not reachable"; fi
  if ! wait_http "http://127.0.0.1:${P2}/" 10; then flunk "second port not reachable"; fi
}

@test "integration: auto-detect ports forwards every non-loopback listener" {
  require_docker
  # Use redis:alpine so the probed port (6379) is unprivileged on the host.
  start_redis_target
  NAME="dpf-bats-auto-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET"
  assert_success
  assert_output_contains "Detected listening ports: 6379"
  assert_output_contains "Forwarding"

  # The helper publishes 6379 on the host with its local port equal to the
  # detected remote port.
  run docker inspect --format '{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostPort}}' "$NAME"
  assert_success
  assert_output "6379"

  # Basic TCP connectivity via redis PING.
  run bash -c 'printf "PING\r\n" | python3 -c "
import socket,sys
s=socket.create_connection((\"127.0.0.1\", 6379), 5)
s.sendall(sys.stdin.buffer.read())
print(s.recv(100).decode().strip())
s.close()"'
  assert_success
  assert_output_contains "PONG"
}

@test "integration: preflight rejects in-use host port and creates no helper" {
  require_docker
  start_nginx_target

  # Occupy a host port from the test runner itself so preflight's net.Listen
  # observes EADDRINUSE. Using Docker port-publishing would be unreliable on
  # Docker Desktop where host-port binding is proxied through a VM.
  BUSY_PORT=$(free_port)
  local blocker_log="/tmp/dpf-blocker-$$.out"

  # Fully detach the python blocker so bats doesn't wait on its file
  # descriptors (redirect stdio, background, disown).
  nohup python3 -c "
import socket, sys, time
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', ${BUSY_PORT}))
s.listen(1)
print('ready'); sys.stdout.flush()
while True: time.sleep(1)
" </dev/null >"$blocker_log" 2>&1 &
  BLOCKER_PID=$!
  disown "$BLOCKER_PID" 2>/dev/null || true

  # Wait for the blocker to report ready (or time out).
  local i
  for i in $(seq 1 20); do
    grep -q '^ready$' "$blocker_log" 2>/dev/null && break
    sleep 0.1
  done

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$BUSY_PORT:80"

  # Stop the blocker and clean up its log regardless of the assertions below.
  kill "$BLOCKER_PID" 2>/dev/null || true
  rm -f "$blocker_log"

  assert_failure
  assert_output_contains "not available"

  run helpers_for_target
  assert_output "0"
}

@test "integration: overlapping request reuses existing helper and exits 0" {
  require_docker
  start_nginx_target
  PA=$(free_port)
  PB=$(free_port)
  NAME="dpf-bats-idem-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PA:80"
  assert_success

  # Overlap on (PA, 80) plus a new pair (PB, 8080) — should no-op and exit 0.
  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PA:80" "$PB:8080"
  assert_success
  assert_output_contains "no action taken"
  assert_output_contains "$NAME"

  run helpers_for_target running
  assert_output "1"
}

@test "integration: cleanup --name removes only the named helper" {
  require_docker
  start_nginx_target
  P1=$(free_port)
  P2=$(free_port)
  KEEP="dpf-bats-keep-$$"
  DROP="dpf-bats-drop-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$KEEP" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$P1:80"
  assert_success

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$DROP" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$P2:80"
  assert_success

  run "$DOCKER_PORT_FORWARD" port-forward cleanup --name "$DROP"
  assert_success
  assert_output_contains "Removed 1 of 1"

  run docker inspect "$DROP"
  assert_failure

  run docker inspect --format '{{.State.Running}}' "$KEEP"
  assert_success
  assert_output "true"
}

@test "integration: cleanup --target scopes removal to one target container" {
  require_docker
  start_nginx_target
  P1=$(free_port)
  NAME="dpf-bats-target-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$P1:80"
  assert_success

  run "$DOCKER_PORT_FORWARD" port-forward cleanup --target "$TARGET"
  assert_success
  assert_output_contains "Removed 1 of 1"

  run docker inspect "$NAME"
  assert_failure
}

@test "integration: cleanup --dry-run lists helpers without removing them" {
  require_docker
  start_nginx_target
  P1=$(free_port)
  NAME="dpf-bats-dryrun-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$P1:80"
  assert_success

  run "$DOCKER_PORT_FORWARD" port-forward cleanup --dry-run --target "$TARGET"
  assert_success
  assert_output_contains "would remove 1"
  assert_output_contains "$NAME"

  run docker inspect --format '{{.State.Running}}' "$NAME"
  assert_success
  assert_output "true"
}

@test "integration: cleanup --name rejects non-port-forward containers" {
  require_docker
  start_nginx_target

  run "$DOCKER_PORT_FORWARD" port-forward cleanup --name "$TARGET"
  assert_failure
  assert_output_contains "is not a port-forward helper"
}

# ---------------------------------------------------------------------------
# UDP support
# ---------------------------------------------------------------------------

# start_udp_echo_target starts a UDP echo server on port 9999 (using
# alpine/socat as both image and implementation) and exports $TARGET.
start_udp_echo_target() {
  export TARGET="bats-udp-${BATS_TEST_NUMBER}-$$"
  docker rm -f "$TARGET" >/dev/null 2>&1 || true
  docker run -d --name "$TARGET" --label "${INTEGRATION_LABEL}=true" \
    alpine/socat -T 3600 UDP-RECVFROM:9999,fork,reuseaddr SYSTEM:cat >/dev/null
}

@test "integration: UDP datagram round-trips through a detached forward" {
  require_docker
  start_udp_echo_target
  PORT=$(free_port)
  NAME="dpf-bats-udp-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PORT:9999/udp"
  assert_success
  assert_output_contains "9999/udp"

  # Helper publishes the UDP port (not TCP).
  run docker inspect --format '{{json .HostConfig.PortBindings}}' "$NAME"
  assert_success
  assert_output_contains "9999/udp"

  # Round-trip a datagram through the forward.
  run bash -c 'python3 -c "
import socket, sys, time
port = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(1.0)
payload = b\"dpf-bats-udp\"
deadline = time.time() + 15
while time.time() < deadline:
    s.sendto(payload, (\"127.0.0.1\", port))
    try:
        data, _ = s.recvfrom(4096)
        if data == payload:
            print(\"ok\")
            sys.exit(0)
    except socket.timeout:
        continue
print(\"no reply\", file=sys.stderr)
sys.exit(1)
" '"$PORT"
  assert_success
  assert_output "ok"
}

@test "integration: --udp-timeout is stored in the helper's socat command" {
  require_docker
  start_udp_echo_target
  PORT=$(free_port)
  NAME="dpf-bats-udp-timeout-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --udp-timeout 123s \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PORT:9999/udp"
  assert_success

  # The helper's Cmd should include the custom -T 123 argument for UDP.
  run docker inspect --format '{{index .Config.Cmd 0}}' "$NAME"
  assert_success
  assert_output_contains "-T 123"
  assert_output_contains "UDP-LISTEN:9999"
}

@test "integration: mixed TCP and UDP in one command publishes both protocols" {
  require_docker
  # Target serves HTTP on 80 AND a UDP echo on 9999 via a shell-supervised
  # pair of processes.
  export TARGET="bats-mixed-${BATS_TEST_NUMBER}-$$"
  docker rm -f "$TARGET" >/dev/null 2>&1 || true
  docker run -d --name "$TARGET" --label "${INTEGRATION_LABEL}=true" --entrypoint sh \
    alpine/socat -c \
    "apk add --quiet nginx >/dev/null 2>&1 || true; nginx -g 'daemon off;' & socat -T 3600 UDP-RECVFROM:9999,fork,reuseaddr SYSTEM:cat & wait" >/dev/null

  # Give nginx a moment to boot.
  sleep 1

  PTCP=$(free_port)
  PUDP=$(free_port)
  NAME="dpf-bats-mixed-$$"

  run "$DOCKER_PORT_FORWARD" port-forward \
    --detach --name "$NAME" \
    --label "${INTEGRATION_LABEL}=true" \
    "container/$TARGET" "$PTCP:80" "$PUDP:9999/udp"
  assert_success

  run docker inspect --format '{{json .HostConfig.PortBindings}}' "$NAME"
  assert_success
  assert_output_contains "80/tcp"
  assert_output_contains "9999/udp"
}
