#!/bin/sh
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf 'must-not-run\n' > "$script_dir/../import-executed.txt"
