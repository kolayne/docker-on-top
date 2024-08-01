#!/bin/sh -e

# A-la moreutils chronic: only print output if the command failed
function chronic() {
  output=`"$@" 2>&1` || (echo -n "$output" >&2; false)
}

cd "$(dirname "$0")"

# File names must not contain characters that `grep -E` will treat specially
VERSION_FILE="docker-on-top_version.txt"
CHANGELOG_FILE="CHANGELOG.md"

if [[ -z "$1" ]]; then
  echo "Specify the version number (without the 'v' prefix) as the first cmd-line argument" >&2
  exit 1
fi

VERSION="$1"

if ! grep -E "^# v$VERSION\$" "$CHANGELOG_FILE" >/dev/null; then
  echo Could not find pattern "\"^# v$VERSION\$\"" in "$CHANGELOG_FILE" >&2
  echo Did you specify the right version number? Did you write the changelog? >&2
  exit 2
fi

echo -n "$VERSION" > "$VERSION_FILE"
git add "$VERSION_FILE" "$CHANGELOG_FILE"

if [[ -z "$(git diff --cached "$CHANGELOG_FILE")" ]]; then
  echo "The changelog file $CHANGELOG_FILE appears unmodified." >&2
  echo "Make sure you write the changelog and specify the release date but do not commit." >&2
  exit 3
fi

if git diff --cached --name-only | grep -v -E "($VERSION_FILE|$CHANGELOG_FILE)"; then
  echo "The following files are to be committed:"
  git diff --cached --name-only
  echo
fi

chronic git commit -m "Version v$VERSION"
git tag "v$VERSION"

echo -n '-dirty' >> "$VERSION_FILE"
git add "$VERSION_FILE"
chronic git commit -m "Restored the '-dirty' version"


git log --compact-summary -4
echo -e '\033[1m'Done. The tag and commits are created. See the log above.
echo -e Everything seems fine? Push to GitHub with '`git push --follow-tags`''\033[0m'
