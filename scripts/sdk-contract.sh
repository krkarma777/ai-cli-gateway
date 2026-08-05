#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
    printf '%s\n' 'usage: scripts/sdk-contract.sh PYTHON NODE JAVASCRIPT' >&2
    exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "${script_dir}/.." && pwd -P)
cd -- "${repository_root}"
exec go run -trimpath ./internal/sdkcontract/cmd/sdk-contract \
    --repository-root "${repository_root}" \
    --python "$1" \
    --node "$2" \
    --javascript "$3"
