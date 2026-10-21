#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# check_release_scope.sh — decide whether a diff has to be validated by the
# goreleaser CI job.
#
# The release pipeline is only ever exercised by a tag push, and a tag push is
# the one operation that cannot be taken back. CI therefore runs
# `goreleaser check` plus a snapshot release on pull requests, but a snapshot
# build cross-compiles six targets, which is wasteful on a diff that cannot
# possibly break it — a markdown or an HTTP-handler change, say. This script is
# the gate that keeps the job off those pull requests.
#
# It answers one question: can this diff change what the release pipeline does?
# The paths it accepts are the ones that feed it — the goreleaser config
# itself, the module and toolchain it compiles with, the files that stamp the
# same build info, the buildinfo package those ldflags name, this script, and
# the workflows that run it. Go source under cmd/ and internal/ is deliberately
# *not* matched: the `docker` CI job already cross-compiles those same sources
# for linux/amd64 and linux/arm64 on every pull request, so the blind spot this
# leaves is darwin- and windows-only code — the compromise made in exchange for
# not running a six-target build on every pull request. Pushes to main do not
# go through the gate at all (see --all), so main is always validated.
#
# Usage:
#   git diff --name-only <base>...HEAD | scripts/check_release_scope.sh
#   scripts/check_release_scope.sh --all
#
# --all reports RUN without reading anything: the caller could not determine
# the diff, or knows the build must be validated regardless. Preferring a
# wasted build over a silently unvalidated release configuration is the
# direction to fail in, so an empty file list is also treated as in scope.
#
# Output: one [RUN]/[SKIP] decision line on stdout, plus `run=true|false`
# written to $GITHUB_OUTPUT when that variable is set (and echoed to stdout
# when it is not, so the script is usable by hand).
#
# Exit code: always 0 — the decision is not itself a check. A job that skips
# because the diff is out of scope is a success, not a failure.
# ---------------------------------------------------------------------------
set -euo pipefail

usage() {
	printf 'usage: %s [--all]\n' "${BASH_SOURCE[0]}" >&2
}

force_all=0
while [ $# -gt 0 ]; do
	case "$1" in
	--all) force_all=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
	esac
	shift
done

# emit_run publishes the decision to the step's outputs when running under
# GitHub Actions, and to stdout otherwise.
emit_run() {
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		printf 'run=%s\n' "$1" >>"$GITHUB_OUTPUT"
	else
		printf 'run=%s\n' "$1"
	fi
}

# release_affecting reports whether one repository-relative path can change
# what the release pipeline produces.
release_affecting() {
	case "$1" in
	# The configuration under validation.
	.goreleaser.yaml) return 0 ;;
	# The module and its toolchain: a dependency or go-directive change
	# alters what every target compiles against.
	go.mod | go.sum) return 0 ;;
	# These stamp the same buildinfo symbols as .goreleaser.yaml, through
	# the same import paths, so a rename in one silently breaks the other.
	Makefile | Dockerfile | Dockerfile.*) return 0 ;;
	# The ldflags targets themselves. A rename here is invisible to the
	# linker: the build succeeds and every release reports "unknown".
	internal/buildinfo/*) return 0 ;;
	# The pipeline: a change to it has to be validated by it.
	.github/workflows/ci.yml | .github/workflows/release.yml) return 0 ;;
	# This gate, so editing it re-runs the job it gates.
	scripts/*) return 0 ;;
	esac
	return 1
}

if [ "$force_all" -eq 1 ]; then
	printf '[RUN] release build forced; the caller did not scope the diff\n'
	emit_run true
	exit 0
fi

matched=""
saw_path=0
while IFS= read -r path || [ -n "$path" ]; do
	[ -n "$path" ] || continue
	saw_path=1
	if release_affecting "$path"; then
		matched="$path"
		break
	fi
done

if [ -z "$matched" ] && [ "$saw_path" -eq 0 ]; then
	printf '[RUN] no changed paths were supplied; validating rather than skipping\n'
	emit_run true
	exit 0
fi

if [ -n "$matched" ]; then
	printf '[RUN] %s can change what the release pipeline produces\n' "$matched"
	emit_run true
	exit 0
fi

printf '[SKIP] no release-affecting path in this diff; the release build cannot differ\n'
emit_run false
