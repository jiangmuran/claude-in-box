#!/usr/bin/env bash
# A minimal stand-in for the `claude` CLI used by clauth_test.go. Supports
# only the three subcommands the package cares about. State (logged-in or
# not) lives under $STUB_STATE_DIR.
set -u
STATE_DIR="${STUB_STATE_DIR:-/tmp/clauth-stub-state}"
mkdir -p "$STATE_DIR"

sub="${1:-} ${2:-}"
case "$sub" in
    "auth status")
        if [[ -f "$STATE_DIR/loggedin" ]]; then
            cat <<EOF
{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"$(cat "$STATE_DIR/email" 2>/dev/null || echo test@example.com)","subscriptionType":"max"}
EOF
        else
            echo '{"loggedIn":false}'
        fi
        ;;

    "auth login")
        echo "Opening browser to sign in…"
        echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?fake=1&code_challenge=stub&state=stubstate"
        printf "Paste code here if prompted > "
        IFS= read -r code
        case "$code" in
            good-*)
                touch "$STATE_DIR/loggedin"
                echo "test@example.com" > "$STATE_DIR/email"
                echo "Signed in."
                exit 0
                ;;
            *)
                echo "Error: invalid code" >&2
                exit 1
                ;;
        esac
        ;;

    "auth logout")
        rm -f "$STATE_DIR/loggedin" "$STATE_DIR/email"
        echo "Logged out."
        exit 0
        ;;

    *)
        echo "stub: unsupported subcommand: $sub" >&2
        exit 2
        ;;
esac
