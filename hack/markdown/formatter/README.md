# Markdown Formatter

## Introduction

These scripts format Markdown documents using Prettier and Remark. It also
supports round-tripping documents through Google Drive while preserving local
YAML frontmatter, if present.

Podman is the default container runtime; set `RUNTIME` to use another compatible
runtime. Google Drive commands additionally require Google Workspace CLI
credentials in `~/.config/gws`. The runner copies those credentials to a
temporary, SELinux-labeled directory, mounts that disposable copy into the
container, and removes it when the operation exits. This prevents refreshed
tokens from mutating the host credentials. The formatter uses the [Google
Workspace CLI](https://github.com/googleworkspace/cli) to download and upload
documents.

The formatter image runs as a non-root user with a read-only root filesystem.
The repository is mounted at `/workdir`, and disposable credentials are mounted
at `/tmp/.config/gws` when needed. Image builds hash the contents of
`hack/markdown` and store the hash in the
`io.machine-config-operator.markdown-hash` image label. An existing image with
a matching label is reused without rebuilding.

Unfortunately, setting up the Google Workspace CLI setup is not user-friendly
and is unnecessarily complicated. Follow these
[setup instructions](https://docs.google.com/document/d/1NegKLQv-4wzl9IR8LDa0IWcWYDiPAhZwGE91u14JYV4/edit?tab=t.0#heading=h.cufdz63lm7n3)
which will get you authenticated. Once authenticated, the CLI will store its
credentials into `~/.config/gws`.

## Usage

Build the image with:

```bash
make image-markdown
```

Format a local document with:

```bash
make format-md MARKDOWN_FILE=docs/example.md
```

Lint Markdown files with:

```bash
make lint-md WHAT=docs
```

Download and format a Google Doc with:

```bash
make pull-md-from-google-drive \
  DOCUMENT_URL="https://docs.google.com/document/d/DOCUMENT_ID/edit" \
  MARKDOWN_FILE=docs/example.md
```

Upload a local Markdown document to Google Docs with:

```bash
make push-md-to-google-drive \
  DOCUMENT_URL="https://docs.google.com/document/d/DOCUMENT_ID/edit" \
  MARKDOWN_FILE=docs/example.md
```

The target Google Doc must already exist, and the authenticated Google account
must have edit permissions for the doc. The upload operation completely replaces
the content of the Google Doc with the local Markdown document body.

Google Drive's Markdown parser does not reliably preserve YAML frontmatter
during uploads. For that reason, the script strips a leading, closed and valid
YAML frontmatter block before uploading. An unclosed or invalid block is treated
as document content. For downloading, the script preserves a closed frontmatter
block from the Markdown file on disk, downloads the Markdown from
Google Drive, and prepends that block to the downloaded Markdown. An unclosed
block produces no frontmatter file. The upload process sets the target Google
Doc to pageless mode.

## Testing

The formatter has BATS tests for its shared library functions, local formatting
transformations, and Google Drive roundtrips. Run all of them with:

```bash
make test-markdown
```

Pass `DOCUMENT_URL` to enable the credentialed roundtrip tests:

```bash
make test-markdown \
  DOCUMENT_URL="https://docs.google.com/document/d/DOCUMENT_ID/edit"
```

Roundtrip tests require both `DOCUMENT_URL` and credentials in
`~/.config/gws`. If either is unavailable, the tests are skipped and report the
reason. The test target mounts the repository at `/workdir` and uses the image's
installed BATS runner; the source tests do not need to be copied into the image.

```bash
make test-markdown
```

## Toolchain

The formatter runs inside a Podman container built from the pinned Hummingbird
Node.js image. It uses:

- [Prettier](https://prettier.io/) for the primary Markdown formatting pass.
- [Remark](https://remark.js.org/) with
  [remark-gfm](https://github.com/remarkjs/remark-gfm) for Markdown parsing,
  GitHub Flavored Markdown support, and lint-oriented normalization.
- [remark-frontmatter](https://github.com/remarkjs/remark-frontmatter) to
  preserve YAML frontmatter as Markdown metadata.
- Custom Remark plugins to identify Mermaid code blocks and remove word
  processor artifacts such as curly quotes, em dashes, ellipses, and
  non-breaking spaces from prose.
- A YAML validator used to distinguish frontmatter from document content during
  uploads.
- Git to resolve the mounted repository root consistently from scripts and BATS
  tests.
- The Google Workspace CLI for Google Drive synchronization.

## Formatting Rules

The formatter applies these rules:

- Wrap prose and formatted lines at 80 columns.
- Use two spaces for indentation and never tabs.
- Wrap prose rather than preserving arbitrary source line lengths.
- Use `-` for unordered list bullets and one-space list indentation.
- Use fenced code blocks and `-` for horizontal rules.
- Preserve YAML frontmatter.
- Prefer resource-style links where supported.
- Automatically identify Mermaid diagrams from their content when a code block
  has no language label. Leading Mermaid directives are ignored when detecting
  the diagram declaration, and explicit non-Mermaid labels are preserved.
- Normalize curly quotes to straight quotes, en dashes to `-`, em dashes to
  `--`, ellipses to `...`, and non-breaking spaces to regular spaces in prose
  text only; YAML frontmatter and fenced code are unchanged.
