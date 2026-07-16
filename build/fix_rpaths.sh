#!/bin/sh
# dylibbundler can leave duplicate LC_RPATH entries behind, which modern
# dyld rejects outright ("duplicate LC_RPATH"). Strip the extras.
set -eu
RPATH='@executable_path/../Frameworks/'
for f in "$@"; do
    n=$(otool -l "$f" | grep -c "path $RPATH" || true)
    while [ "$n" -gt 1 ]; do
        install_name_tool -delete_rpath "$RPATH" "$f"
        n=$((n - 1))
    done
done
