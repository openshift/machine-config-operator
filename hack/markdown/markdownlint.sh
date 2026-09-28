#!/bin/bash -e

search_path="${LINT_TARGET:-.}"

lint_files=$(find "$search_path" -type f -name "*.md" \
	-not -path "./.git/*" \
	-not -path "./vendor/*" \
	-not -path "./_output/*")

if [ -z "$lint_files" ]; then
	echo "No markdown files found to lint."
	exit 0
fi

lint_files=$(echo "$lint_files" | tr '\n' ',')

markdownlint-cli2 '{'"$lint_files"'}'
