#!/bin/sh
# Fail the release when CHANGELOG.md has no section for the tag version.
# Usage: scripts/check-changelog.sh <versão>   (ex.: 0.0.10, sem o "v")
# Exit 0 when CHANGELOG.md has a line starting with "## <versão> — " followed
# by at least one "- " item before the next "## " line; exit 1 otherwise.
# POSIX sh: runs on Linux (dash) and macOS (bash as sh).
set -eu

version="${1:?Uso: $0 <versão> (ex.: 0.0.10)}"
if [ ! -f CHANGELOG.md ]; then
	echo "CHANGELOG.md não tem a seção da versão $version" >&2
	exit 1
fi
if awk -v version="$version" '
	BEGIN { hdr = "## " version " — "; in_section = 0; items = 0 }
	substr($0, 1, 3) == "## " {
		if (in_section) { exit }
		if (substr($0, 1, length(hdr)) == hdr) { in_section = 1 }
		next
	}
	in_section && substr($0, 1, 2) == "- " { items++ }
	END { exit !(in_section && items > 0) }
' CHANGELOG.md; then
	exit 0
else
	echo "CHANGELOG.md não tem a seção da versão $version" >&2
	exit 1
fi
