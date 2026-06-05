#!/usr/bin/env python3
"""
Create Polarion test case from validated draft JSON.

Usage:
    python3 create_polarion_tc.py <draft.json> [--dry-run]

Workflow:
1. Validate draft (must pass validate_tc.py)
2. POST testcase (basic fields)
3. PATCH custom fields
4. POST teststeps (blank slate strategy)
5. Return OCP-xxxxx URL
"""

import json
import os
import sys
from typing import Dict, Any
from polarion_client import PolarionClient


def load_env():
    """Load .env file from repo root"""
    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(os.path.dirname(script_dir))
    env_file = os.path.join(repo_root, '.env')

    if not os.path.exists(env_file):
        print(f"Error: .env not found at {env_file}", file=sys.stderr)
        print("Run: .claude/scripts/setup.sh", file=sys.stderr)
        sys.exit(1)

    env_vars = {}
    with open(env_file) as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith('#') and '=' in line:
                key, value = line.split('=', 1)
                env_vars[key] = value

    return env_vars


def validate_draft(draft: Dict) -> bool:
    """Run canonical validation from validate_tc.py"""
    from validate_tc import TCValidator
    validator = TCValidator(strict=True)
    is_valid = validator.validate(draft)
    validator.print_report()
    return is_valid


def create_test_case(
    client: PolarionClient,
    draft: Dict,
    project: str = 'OSE'
) -> Dict[str, Any]:
    """Create test case with full workflow"""

    # Step 1: Create base test case
    print("Creating test case...", file=sys.stderr)

    workitem_data = {
        "data": [{
            "type": "workitems",
            "attributes": {
                "type": "testcase",
                "title": draft['title'],
                "description": {
                    "type": "text/html",
                    "value": draft.get('description', '').replace("\n", "<br/>")
                },
                "status": "draft"
            }
        }]
    }

    result = client._make_request(
        "POST",
        f"projects/{project}/workitems",
        data=workitem_data
    )

    if "error" in result:
        return {
            "status": "failed",
            "error": f"Failed to create test case: {result['error']}"
        }

    test_case_data = result.get("data", [{}])[0] if isinstance(result.get("data"), list) else result.get("data", {})
    test_case_id = test_case_data.get("id", "unknown")

    print(f"✓ Created: {test_case_id}", file=sys.stderr)

    warnings = []

    # Step 2: PATCH custom fields
    print("Patching custom fields...", file=sys.stderr)

    # Build custom fields with correct types
    # plannedin expects array, others expect strings
    custom_fields = {}

    if draft.get('component'):
        custom_fields["casecomponent"] = draft.get('component')
    if draft.get('sub_team'):
        custom_fields["subteam2"] = draft.get('sub_team')
    if draft.get('products'):
        custom_fields["mcoproducts"] = draft.get('products')
    if draft.get('test_type'):
        custom_fields["casetesttype"] = draft.get('test_type')
    if draft.get('version'):
        custom_fields["plannedin"] = [draft.get('version')]  # Array format
    if draft.get('trello_jira'):
        custom_fields["linkedworkitems"] = draft.get('trello_jira')  # String format

    if custom_fields:
        patch_data = {
            "data": {
                "type": "workitems",
                "id": f"{project}/{test_case_id}",
                "attributes": custom_fields
            }
        }

        patch_result = client._make_request(
            "PATCH",
            f"projects/{project}/workitems/{test_case_id}",
            data=patch_data
        )

        if "error" in patch_result:
            warnings.append(f"Failed to patch custom fields: {patch_result['error']}")
            print(f"⚠️  Warning: Failed to patch custom fields: {patch_result['error']}", file=sys.stderr)
        else:
            print(f"✓ Patched {len(custom_fields)} custom fields", file=sys.stderr)

    # Step 3: POST test steps (blank slate strategy)
    test_steps = draft.get('test_steps', [])
    if test_steps:
        print(f"Adding {len(test_steps)} test steps...", file=sys.stderr)

        steps_data = []
        for step in test_steps:
            step_obj = {
                "type": "teststeps",
                "attributes": {
                    "keys": ["step", "expectedResult"],
                    "values": [
                        {
                            "type": "text/html",
                            "value": step.get("step", "").replace("\n", "<br/>")
                        },
                        {
                            "type": "text/html",
                            "value": step.get("expected_result", "").replace("\n", "<br/>")
                        }
                    ]
                }
            }
            steps_data.append(step_obj)

        steps_result = client._make_request(
            "POST",
            f"projects/{project}/workitems/{test_case_id}/teststeps",
            data={"data": steps_data}
        )

        if "error" in steps_result:
            warnings.append(f"Failed to add test steps: {steps_result['error']}")
            print(f"⚠️  Warning: Failed to add test steps: {steps_result['error']}", file=sys.stderr)
        else:
            created_steps = steps_result.get("data", [])
            print(f"✓ Added {len(created_steps)} test steps", file=sys.stderr)

    result = {
        "status": "partial_success" if warnings else "success",
        "test_case_id": test_case_id,
        "title": draft['title'],
        "project": project,
        "url": f"{client.url}/polarion/#/project/{project}/workitem?id={test_case_id}"
    }
    if warnings:
        result["warnings"] = warnings
    return result


def main():
    import argparse

    parser = argparse.ArgumentParser(description='Create Polarion test case from validated draft')
    parser.add_argument('draft_file', help='Path to draft JSON file')
    parser.add_argument('--project', default='OSE', help='Polarion project ID (default: OSE)')
    parser.add_argument('--dry-run', action='store_true', help='Validate only, do not create')

    args = parser.parse_args()

    # Load draft
    try:
        with open(args.draft_file) as f:
            draft = json.load(f)
    except FileNotFoundError:
        print(f"Error: File not found: {args.draft_file}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"Error: Invalid JSON: {e}", file=sys.stderr)
        sys.exit(1)

    # Validate
    print("Validating draft...", file=sys.stderr)
    if not validate_draft(draft):
        print("\nFix validation errors and try again.", file=sys.stderr)
        sys.exit(1)

    print("✓ Validation passed\n", file=sys.stderr)

    # Show summary
    print("=" * 80, file=sys.stderr)
    print("TEST CASE SUMMARY", file=sys.stderr)
    print("=" * 80, file=sys.stderr)
    print(f"Title: {draft['title']}", file=sys.stderr)
    print(f"Component: {draft.get('component', 'NOT SET')}", file=sys.stderr)
    print(f"Sub Team: {draft.get('sub_team', 'NOT SET')}", file=sys.stderr)
    print(f"Products: {draft.get('products', 'NOT SET')}", file=sys.stderr)
    print(f"Test Type: {draft.get('test_type', 'NOT SET')}", file=sys.stderr)
    print(f"Version: {draft.get('version', 'NOT SET')}", file=sys.stderr)
    print(f"Jira: {draft.get('trello_jira', 'NOT SET')}", file=sys.stderr)
    print(f"Test Steps: {len(draft.get('test_steps', []))}", file=sys.stderr)
    print("=" * 80, file=sys.stderr)
    print()

    if args.dry_run:
        print("✓ DRY RUN: Draft is valid and ready for creation", file=sys.stderr)
        sys.exit(0)

    # Load environment
    env = load_env()
    polarion_url = env.get('POLARION_URL', 'https://polarion.engineering.redhat.com')
    polarion_token = env.get('POLARION_TOKEN', '')

    if not polarion_token or polarion_token == 'your_personal_access_token_here':
        print("Error: POLARION_TOKEN not configured in .env", file=sys.stderr)
        print("Generate token at: https://polarion.engineering.redhat.com/polarion/#/user/me", file=sys.stderr)
        sys.exit(1)

    # Create client
    client = PolarionClient(
        url=polarion_url,
        token=polarion_token,
        verify_ssl=True
    )

    # Create test case
    result = create_test_case(client, draft, args.project)

    if result['status'] == 'failed':
        print(f"\n❌ Error: {result['error']}", file=sys.stderr)
        sys.exit(1)

    print("\n" + "=" * 80, file=sys.stderr)
    if result['status'] == 'partial_success':
        print("⚠️  TEST CASE CREATED WITH WARNINGS", file=sys.stderr)
        print("   Run update_polarion_tc.py to patch missing fields/steps", file=sys.stderr)
    else:
        print("✓ TEST CASE CREATED SUCCESSFULLY", file=sys.stderr)
    print("=" * 80, file=sys.stderr)
    print(f"ID: {result['test_case_id']}", file=sys.stderr)
    print(f"URL: {result['url']}", file=sys.stderr)
    print("=" * 80, file=sys.stderr)

    # Output JSON result to stdout
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
