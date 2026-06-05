#!/usr/bin/env python3
"""
Create Polarion TC from Jira issue + GitHub PR (hybrid mode).

Usage:
    python3 create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691 --comment 4054832254
    python3 create_tc_hybrid.py MCO-2221 --from-pr 5691

Workflow:
1. Fetch Jira issue (summary, description, version, acceptance criteria)
2. Fetch GitHub PR comment (test steps)
3. Merge into draft
4. Validate
5. Create Polarion TC
"""

import json
import os
import re
import sys
import subprocess


def run_script(script_name: str, args: list) -> dict:
    """Run another script and capture JSON output"""
    script_dir = os.path.dirname(os.path.abspath(__file__))
    script_path = os.path.join(script_dir, script_name)

    cmd = ['python3', script_path] + args

    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            check=True
        )

        # Parse JSON from stdout
        return json.loads(result.stdout)

    except subprocess.CalledProcessError as e:
        print(f"Error running {script_name}:", file=sys.stderr)
        print(e.stderr, file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"Error parsing JSON from {script_name}:", file=sys.stderr)
        print(e, file=sys.stderr)
        sys.exit(1)


def merge_jira_pr_drafts(jira_draft: dict, pr_draft: dict) -> dict:
    """
    Merge Jira and PR drafts.

    Priority:
    - Metadata (title, version, jira): From Jira
    - Description: From Jira
    - Test steps: From PR
    - Acceptance criteria: From Jira (append to description)
    """

    # Start with Jira draft
    merged = jira_draft.copy()

    # Override test steps from PR
    if pr_draft.get('test_steps'):
        merged['test_steps'] = pr_draft['test_steps']

    # Merge metadata
    if 'metadata' not in merged:
        merged['metadata'] = {}

    merged['metadata']['source_jira'] = jira_draft.get('trello_jira', '')
    merged['metadata']['source_pr'] = pr_draft.get('metadata', {}).get('source_pr', '')

    # Append acceptance criteria to description
    ac = jira_draft.get('metadata', {}).get('acceptance_criteria', '')
    if ac:
        merged['description'] = f"{merged.get('description', '')}\n\nAcceptance Criteria:\n{ac}"

    return merged


def main():
    import argparse

    parser = argparse.ArgumentParser(
        description='Create Polarion TC from Jira + GitHub PR (hybrid)',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Example:
  python3 create_tc_hybrid.py OCPBUGS-74223 --from-pr 5691 --comment 4054832254

Workflow:
  1. Fetch Jira issue (metadata, acceptance criteria)
  2. Fetch GitHub PR comment (test steps)
  3. Merge drafts
  4. Validate
  5. Create Polarion TC

Output:
  OCP-xxxxx URL
        """
    )

    parser.add_argument('jira_key', help='Jira issue key (e.g., OCPBUGS-74223, MCO-2221)')
    parser.add_argument('--from-pr', dest='pr_number', type=int, required=True,
                       help='GitHub PR number')
    parser.add_argument('--comment', type=int,
                       help='GitHub comment ID (optional - will find QE comment)')
    parser.add_argument('--repo', default='openshift/machine-config-operator',
                       help='GitHub repository (default: openshift/machine-config-operator)')
    parser.add_argument('--dry-run', action='store_true',
                       help='Validate and show summary without creating')

    args = parser.parse_args()

    if not re.fullmatch(r'[A-Z][A-Z0-9]+-\d+', args.jira_key):
        print(f"Error: invalid Jira key format: {args.jira_key}", file=sys.stderr)
        sys.exit(1)

    print(f"=== Hybrid TC Creation: {args.jira_key} + PR #{args.pr_number} ===", file=sys.stderr)
    print()

    # Step 1: Fetch Jira issue
    print(f"[1/5] Fetching Jira issue: {args.jira_key}...", file=sys.stderr)
    jira_draft = run_script('fetch_jira.py', [args.jira_key])
    print(f"      ✓ Title: {jira_draft['title']}", file=sys.stderr)
    print(f"      ✓ Version: {jira_draft.get('version', 'NOT SET')}", file=sys.stderr)
    print()

    # Step 2: Fetch GitHub PR comment
    print(f"[2/5] Fetching GitHub PR #{args.pr_number}...", file=sys.stderr)
    pr_args = ['--repo', args.repo, '--pr', str(args.pr_number)]
    if args.comment:
        pr_args.extend(['--comment', str(args.comment)])

    pr_draft = run_script('fetch_github_pr.py', pr_args)
    pr_steps = pr_draft.get('test_steps', [])
    print(f"      ✓ Test Steps: {len(pr_steps)}", file=sys.stderr)
    if not pr_steps:
        print("      ❌ No test steps parsed from PR comment; aborting.", file=sys.stderr)
        sys.exit(1)
    print()

    # Step 3: Merge drafts
    print("[3/5] Merging Jira + PR data...", file=sys.stderr)
    merged_draft = merge_jira_pr_drafts(jira_draft, pr_draft)

    # Save merged draft
    output_dir = '/tmp/test-specs/drafts'
    os.makedirs(output_dir, exist_ok=True)
    draft_file = os.path.join(output_dir, f"{args.jira_key}-pr{args.pr_number}.json")

    with open(draft_file, 'w') as f:
        json.dump(merged_draft, f, indent=2)

    print(f"      ✓ Merged draft: {draft_file}", file=sys.stderr)
    print()

    # Step 4: Validate
    print("[4/5] Validating draft...", file=sys.stderr)
    validate_result = subprocess.run(
        ['python3', 'validate_tc.py', draft_file],
        cwd=os.path.dirname(os.path.abspath(__file__)),
        capture_output=True,
        text=True
    )

    if validate_result.returncode != 0:
        print("      ❌ Validation failed:", file=sys.stderr)
        if validate_result.stdout:
            print(validate_result.stdout, file=sys.stderr)
        if validate_result.stderr:
            print(validate_result.stderr, file=sys.stderr)
        sys.exit(1)

    print("      ✓ Validation passed", file=sys.stderr)
    print()

    # Step 5: Create (or dry-run)
    if args.dry_run:
        print("[5/5] DRY RUN - Showing summary...", file=sys.stderr)
        print()
        result = subprocess.run(
            ['python3', 'create_polarion_tc.py', draft_file, '--dry-run'],
            cwd=os.path.dirname(os.path.abspath(__file__))
        )
        sys.exit(result.returncode)
    else:
        print("[5/5] Creating Polarion TC...", file=sys.stderr)
        print()
        result = subprocess.run(
            ['python3', 'create_polarion_tc.py', draft_file],
            cwd=os.path.dirname(os.path.abspath(__file__))
        )
        sys.exit(result.returncode)


if __name__ == '__main__':
    main()
