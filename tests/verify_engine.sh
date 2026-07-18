#!/usr/bin/env bash
# verify_engine.sh - Test harness for dopt local archive extraction and layout handling

set -euo pipefail

# Find the root of the project
PROJECT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DOPT_BIN="$PROJECT_ROOT/dopt"
TEST_DIR=$(mktemp -d -t dopt-test-XXXXXXXX)

echo "[*] Setting up mock test environment in $TEST_DIR"

cleanup() {
    echo ""
    echo "[*] Waiting 10 seconds before cleaning up mock files to allow inspection..."
    sleep 10
    echo "[*] Cleaning up mock test environment and system files..."
    rm -rf "$TEST_DIR"
    sudo rm -f "/usr/local/bin/mockbin"
    sudo rm -rf "/opt/mockapp"
    sudo rm -f "/usr/share/applications/mockapp.desktop"
    echo "[+] Cleanup complete. Test environment is restored."
}
trap cleanup EXIT

# 1. Create Mock App Manifest
cat << 'EOF' > "$TEST_DIR/mockapp.json"
{
  "app_id": "mockapp",
  "name": "Mock Application",
  "comment": "Mock app for testing dopt",
  "default_install_dir": "/opt/mockapp",
  "binary_pattern": "mockbin*",
  "symlink_as": "mockbin",
  "categories": "Utility;"
}
EOF

# 2. Create Naked Layout Archive
echo "[*] Creating Naked Layout Archive..."
mkdir -p "$TEST_DIR/naked"
cat << 'EOF' > "$TEST_DIR/naked/mockbin"
#!/usr/bin/env bash
echo "Hello from Naked MockBin!"
EOF
chmod +x "$TEST_DIR/naked/mockbin"
touch "$TEST_DIR/naked/mockapp.png" # mock icon
cd "$TEST_DIR/naked" && tar -czf "$TEST_DIR/mockapp-naked.tar.gz" *
cd "$PROJECT_ROOT"

# 3. Create Nested Layout Archive
echo "[*] Creating Nested Layout Archive..."
mkdir -p "$TEST_DIR/nested/mockapp-v1.0"
cat << 'EOF' > "$TEST_DIR/nested/mockapp-v1.0/mockbin"
#!/usr/bin/env bash
echo "Hello from Nested MockBin!"
EOF
chmod +x "$TEST_DIR/nested/mockapp-v1.0/mockbin"
touch "$TEST_DIR/nested/mockapp-v1.0/mockapp.png" # mock icon
cd "$TEST_DIR/nested" && tar -czf "$TEST_DIR/mockapp-nested.tar.gz" *
cd "$PROJECT_ROOT"

# 4. Test Naked Layout
echo ""
echo "======================================"
echo "RUNNING TEST: Naked Layout Archive"
echo "======================================"
sudo "$DOPT_BIN" -m "$TEST_DIR/mockapp.json" -f "$TEST_DIR/mockapp-naked.tar.gz" -i

if [ -f "/usr/local/bin/mockbin" ] && [ -f "/opt/mockapp/mockbin" ]; then
    echo "[+] Naked Layout Test PASSED!"
else
    echo "[-] Naked Layout Test FAILED!"
    exit 1
fi

# 5. Test Nested Layout
echo ""
echo "======================================"
echo "RUNNING TEST: Nested Layout Archive"
echo "======================================"
sudo "$DOPT_BIN" -m "$TEST_DIR/mockapp.json" -f "$TEST_DIR/mockapp-nested.tar.gz" -i

if [ -f "/usr/local/bin/mockbin" ] && [ -f "/opt/mockapp/mockbin" ]; then
    echo "[+] Nested Layout Test PASSED!"
else
    echo "[-] Nested Layout Test FAILED!"
    exit 1
fi

echo ""
echo "[*] All layout tests passed successfully."
