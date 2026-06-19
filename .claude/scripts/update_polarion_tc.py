#!/usr/bin/env python3
"""
Update existing Polarion test case.

Usage:
    python3 update_polarion_tc.py OCP-88941 --title "New title"
    python3 update_polarion_tc.py OCP-88941 --fix-metadata --component "Machine Config Operator"
    python3 update_polarion_tc.py OCP-88941 --steps-file /tmp/test-specs/steps/OCP-88941.json

Fields that can be updated via REST API (token auth):
- title, description, status, severity
- Custom fields: component, sub_team, products, test_type, version, trello_jira

Test steps:
- REST API limitation: Can only POST steps to NEW test cases (blank slate)
- For EXISTING test cases: Requires SOAP API with username/password
- Set POLARION_USERNAME and POLARION_PASSWORD in .env for test step updates
"""

import json
import os
import sys
from typing import Dict, Any, Optional
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
                env_vars[key] = value.strip('"').strip("'")

    return env_vars


def parse_test_case_id(tc_id: str) -> tuple:
    """Parse test case ID to extract project and ID"""
    import re
    match = re.match(r'^([A-Z]+)-(\d+)$', tc_id)
    if not match:
        raise ValueError(f"Invalid test case ID format: {tc_id}")

    prefix = match.group(1)
    project_map = {
        'OCP': 'OSE',
        'OSE': 'OSE',
    }

    project = project_map.get(prefix, 'OSE')
    return project, tc_id


def update_test_case_fields(
    client: PolarionClient,
    tc_id: str,
    project: str,
    **kwargs
) -> Dict[str, Any]:
    """
    Update test case fields via REST API.

    Supported fields:
    - title, description, status, severity (basic fields)
    - component, sub_team, products, test_type, version, trello_jira (custom fields)
    """

    # Build attributes dict
    attributes = {}

    # Basic fields
    if kwargs.get('title'):
        attributes['title'] = kwargs['title']

    if kwargs.get('description'):
        attributes['description'] = {
            'type': 'text/html',
            'value': kwargs['description'].replace('\n', '<br/>')
        }

    if kwargs.get('status'):
        attributes['status'] = kwargs['status']

    if kwargs.get('severity'):
        attributes['severity'] = kwargs['severity']

    # Custom fields (map friendly names to Polarion field names)
    custom_field_map = {
        'component': 'casecomponent',
        'sub_team': 'subteam2',
        'products': 'mcoproducts',
        'test_type': 'casetesttype',
        'version': 'plannedin',
        'trello_jira': 'linkedworkitems',
    }

    for friendly_name, polarion_name in custom_field_map.items():
        if kwargs.get(friendly_name):
            # plannedin expects array, others expect strings
            if polarion_name == 'plannedin':
                attributes[polarion_name] = [kwargs[friendly_name]]
            else:
                attributes[polarion_name] = kwargs[friendly_name]

    if not attributes:
        return {
            'status': 'failed',
            'error': 'No fields provided to update'
        }

    # Build PATCH request
    update_data = {
        'data': {
            'type': 'workitems',
            'id': f'{project}/{tc_id}',
            'attributes': attributes
        }
    }

    result = client._make_request(
        'PATCH',
        f'projects/{project}/workitems/{tc_id}',
        data=update_data
    )

    if 'error' in result:
        return {
            'status': 'failed',
            'error': result['error']
        }

    return {
        'status': 'success',
        'message': f'Updated {len(attributes)} field(s)',
        'updated_fields': list(attributes.keys())
    }


def update_test_steps(
    client: PolarionClient,
    tc_id: str,
    project: str,
    steps_file: str,
    force_soap: bool = False
) -> Dict[str, Any]:
    """
    Update test steps.

    REST API Limitation:
    - Can only POST steps to NEW test cases (blank slate)
    - For EXISTING test cases with steps: MUST use SOAP API

    SOAP API Requirements:
    - POLARION_USERNAME and POLARION_PASSWORD must be set in .env
    - Uses Basic Auth instead of token
    """

    # Load steps from file
    try:
        with open(steps_file) as f:
            steps_data = json.load(f)
    except FileNotFoundError:
        return {
            'status': 'failed',
            'error': f'Steps file not found: {steps_file}'
        }
    except json.JSONDecodeError as e:
        return {
            'status': 'failed',
            'error': f'Invalid JSON in steps file: {e}'
        }

    # Ensure steps is a list
    if isinstance(steps_data, dict):
        steps = steps_data.get('test_steps', [])
    elif isinstance(steps_data, list):
        steps = steps_data
    else:
        return {
            'status': 'failed',
            'error': 'Steps file must contain array or object with test_steps key'
        }

    # Validate steps before sending to Polarion
    from validate_tc import TCValidator
    validator = TCValidator(strict=True)
    validator._validate_test_steps(steps)
    if validator.errors:
        validator.print_report()
        return {
            'status': 'failed',
            'error': 'Steps failed validation; fix the steps file before updating Polarion'
        }

    # Use add_test_steps from PolarionClient (handles REST vs SOAP logic)
    # Note: For existing TCs, this will attempt REST first, then fall back to SOAP
    result = client.add_test_steps(tc_id, steps, project, force_soap=force_soap)

    return result


def fix_metadata_fields(
    client: PolarionClient,
    tc_id: str,
    project: str,
    component: Optional[str] = None,
    sub_team: Optional[str] = None,
    products: Optional[str] = None,
    test_type: Optional[str] = None,
    version: Optional[str] = None,
    trello_jira: Optional[str] = None
) -> Dict[str, Any]:
    """
    Set metadata fields to MCO defaults (or provided values).

    Note: This overwrites existing values. Use individual field flags
    (--component, --sub-team, etc.) without --fix-metadata to update
    only specific fields.
    """

    # Default values for MCO TCs
    defaults = {
        'component': component or 'Machine Config Operator',
        'sub_team': sub_team or 'MCO',
        'products': products or 'OCP',
        'test_type': test_type or 'Functional',
        'version': version,  # No default - should be specified
        'trello_jira': trello_jira,  # No default - should be specified
    }

    # Filter out None values
    updates = {k: v for k, v in defaults.items() if v is not None}

    return update_test_case_fields(client, tc_id, project, **updates)


def main():
    import argparse

    parser = argparse.ArgumentParser(
        description='Update existing Polarion test case',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Update title
  python3 update_polarion_tc.py OCP-88941 --title "[MCO][OCPBUGS-83830] New title"

  # Fix metadata fields
  python3 update_polarion_tc.py OCP-88941 --fix-metadata --version 4.22 --trello-jira OCPBUGS-83830

  # Update test steps (requires SOAP credentials for existing TC)
  python3 update_polarion_tc.py OCP-88941 --steps-file /tmp/test-specs/steps/OCP-88941.json

  # Update multiple fields
  python3 update_polarion_tc.py OCP-88941 \\
    --title "New title" \\
    --status approved \\
    --component "Machine Config Operator"

REST API Limitation for Test Steps:
  - Can only add steps to NEW test cases (blank slate)
  - For EXISTING test cases: Requires SOAP API with username/password
  - Set POLARION_USERNAME and POLARION_PASSWORD in .env
        """
    )

    parser.add_argument('tc_id', help='Test case ID (e.g., OCP-88941)')
    parser.add_argument('--project', default='OSE', help='Polarion project (default: OSE)')

    # Basic fields
    parser.add_argument('--title', help='Update title')
    parser.add_argument('--description', help='Update description')
    parser.add_argument('--status', help='Update status (draft, approved, etc.)')
    parser.add_argument('--severity', help='Update severity')

    # Custom fields
    parser.add_argument('--component', help='Update component')
    parser.add_argument('--sub-team', dest='sub_team', help='Update sub team')
    parser.add_argument('--products', help='Update products')
    parser.add_argument('--test-type', dest='test_type', help='Update test type')
    parser.add_argument('--version', help='Update version')
    parser.add_argument('--trello-jira', dest='trello_jira', help='Update Trello/Jira')

    # Batch fix
    parser.add_argument('--fix-metadata', action='store_true',
                       help='Fix all empty metadata fields with defaults')

    # Test steps
    parser.add_argument('--steps-file', help='Update test steps from JSON file')
    parser.add_argument('--force-soap', action='store_true',
                       help='Force SOAP API for test steps (requires credentials)')

    # Dry run
    parser.add_argument('--dry-run', action='store_true',
                       help='Show what would be updated without making changes')

    args = parser.parse_args()

    # Parse project from TC ID
    try:
        project, tc_id = parse_test_case_id(args.tc_id)
        if args.project != 'OSE':
            project = args.project
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    # Load environment
    env = load_env()
    polarion_url = env.get('POLARION_URL', 'https://polarion.engineering.redhat.com')
    polarion_token = env.get('POLARION_TOKEN', '')
    polarion_username = env.get('POLARION_USERNAME')
    polarion_password = env.get('POLARION_PASSWORD')

    if not polarion_token or polarion_token == 'your_personal_access_token_here':
        print("Error: POLARION_TOKEN not configured in .env", file=sys.stderr)
        print("Generate token at: https://polarion.engineering.redhat.com/polarion/#/user/me", file=sys.stderr)
        sys.exit(1)

    # Create client
    client = PolarionClient(
        url=polarion_url,
        token=polarion_token,
        verify_ssl=True,
        username=polarion_username,
        password=polarion_password
    )

    print(f"Updating test case: {tc_id} (project: {project})", file=sys.stderr)
    print()

    # Collect updates
    updates_made = False

    # Fix metadata
    if args.fix_metadata:
        print("Fixing metadata fields...", file=sys.stderr)

        if args.dry_run:
            print("DRY RUN: Would update metadata fields:", file=sys.stderr)
            print(f"  component: {args.component or 'Machine Config Operator'}", file=sys.stderr)
            print(f"  sub_team: {args.sub_team or 'MCO'}", file=sys.stderr)
            print(f"  products: {args.products or 'OCP'}", file=sys.stderr)
            print(f"  test_type: {args.test_type or 'Functional'}", file=sys.stderr)
            print(f"  version: {args.version or 'NOT SET'}", file=sys.stderr)
            print(f"  trello_jira: {args.trello_jira or 'NOT SET'}", file=sys.stderr)
        else:
            result = fix_metadata_fields(
                client, tc_id, project,
                component=args.component,
                sub_team=args.sub_team,
                products=args.products,
                test_type=args.test_type,
                version=args.version,
                trello_jira=args.trello_jira
            )

            if result['status'] == 'success':
                print(f"✓ {result['message']}", file=sys.stderr)
                print(f"  Updated: {', '.join(result['updated_fields'])}", file=sys.stderr)
                updates_made = True
            else:
                print(f"❌ Error: {result['error']}", file=sys.stderr)
                sys.exit(1)

        print()

    # Update individual fields
    field_updates = {}
    if args.title:
        field_updates['title'] = args.title
    if args.description:
        field_updates['description'] = args.description
    if args.status:
        field_updates['status'] = args.status
    if args.severity:
        field_updates['severity'] = args.severity
    if args.component and not args.fix_metadata:
        field_updates['component'] = args.component
    if args.sub_team and not args.fix_metadata:
        field_updates['sub_team'] = args.sub_team
    if args.products and not args.fix_metadata:
        field_updates['products'] = args.products
    if args.test_type and not args.fix_metadata:
        field_updates['test_type'] = args.test_type
    if args.version and not args.fix_metadata:
        field_updates['version'] = args.version
    if args.trello_jira and not args.fix_metadata:
        field_updates['trello_jira'] = args.trello_jira

    if field_updates:
        print("Updating fields...", file=sys.stderr)

        if args.dry_run:
            print("DRY RUN: Would update:", file=sys.stderr)
            for key, value in field_updates.items():
                print(f"  {key}: {value}", file=sys.stderr)
        else:
            result = update_test_case_fields(client, tc_id, project, **field_updates)

            if result['status'] == 'success':
                print(f"✓ {result['message']}", file=sys.stderr)
                print(f"  Updated: {', '.join(result['updated_fields'])}", file=sys.stderr)
                updates_made = True
            else:
                print(f"❌ Error: {result['error']}", file=sys.stderr)
                sys.exit(1)

        print()

    # Update test steps
    if args.steps_file:
        print("Updating test steps...", file=sys.stderr)

        if args.dry_run:
            print(f"DRY RUN: Would update test steps from: {args.steps_file}", file=sys.stderr)
            print("Note: Requires SOAP API for existing TCs", file=sys.stderr)
        else:
            # Check for SOAP credentials if force_soap or likely needed
            if not polarion_username or not polarion_password:
                print("⚠️  Warning: POLARION_USERNAME/PASSWORD not set", file=sys.stderr)
                print("   Test step updates on existing TCs require SOAP API", file=sys.stderr)
                print("   Set credentials in .env to enable test step updates", file=sys.stderr)
                print()

            result = update_test_steps(
                client, tc_id, project,
                args.steps_file,
                force_soap=args.force_soap
            )

            if result['status'] == 'success':
                print(f"✓ {result['message']}", file=sys.stderr)
                if result.get('method'):
                    print(f"  Method: {result['method']}", file=sys.stderr)
                updates_made = True
            else:
                print(f"❌ Error: {result['error']}", file=sys.stderr)
                if 'soap_error' in result:
                    print(f"   SOAP Error: {result['soap_error']}", file=sys.stderr)
                sys.exit(1)

        print()

    # Summary
    if args.dry_run:
        print("✓ DRY RUN COMPLETE - No changes made", file=sys.stderr)
    elif updates_made:
        print("=" * 80, file=sys.stderr)
        print("✓ UPDATE COMPLETE", file=sys.stderr)
        print("=" * 80, file=sys.stderr)
        print(f"Test Case: {tc_id}", file=sys.stderr)
        print(f"URL: {polarion_url}/polarion/#/project/{project}/workitem?id={tc_id}", file=sys.stderr)
        print("=" * 80, file=sys.stderr)
    else:
        print("No updates specified. Use --help for usage.", file=sys.stderr)
        sys.exit(1)


if __name__ == '__main__':
    main()
