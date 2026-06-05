#!/usr/bin/env python3
"""
Fetch Polarion test case and output in multiple formats.

Usage:
    python3 fetch_polarion.py OCP-88122
    python3 fetch_polarion.py OSE-12345 --project OSE

Output:
    - /tmp/test-specs/OCP-88122.txt (format for /automate-test)
    - JSON to stdout (title, steps, custom fields)
"""

import json
import os
import sys
import re
from typing import Dict, Any, List
from polarion_client import PolarionClient


def load_env():
    """Load .env file from repo root"""
    # Assume script is in .claude/scripts/, so repo root is ../..
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


def parse_test_case_id(tc_id: str) -> tuple:
    """Parse test case ID to extract project and ID"""
    # Format: OCP-88122 or OSE-12345
    match = re.match(r'^([A-Z]+)-(\d+)$', tc_id)
    if not match:
        raise ValueError(f"Invalid test case ID format: {tc_id}")

    prefix = match.group(1)
    number = match.group(2)

    # Map prefix to project
    project_map = {
        'OCP': 'OSE',  # OpenShift project in Polarion is OSE
        'OSE': 'OSE',
    }

    project = project_map.get(prefix, 'OSE')
    return project, tc_id


def extract_test_steps(data: Dict) -> List[Dict[str, str]]:
    """Extract test steps from Polarion response"""
    steps = []

    # Test steps are in included section
    included = data.get('included', [])
    for item in included:
        if item.get('type') == 'teststeps':
            attrs = item.get('attributes', {})
            values = attrs.get('values', [])

            # Values array typically has [step, expectedResult]
            step_text = ''
            expected_text = ''

            if len(values) >= 1:
                step_value = values[0]
                if isinstance(step_value, dict):
                    step_text = html_to_text(step_value.get('value', ''))

            if len(values) >= 2:
                expected_value = values[1]
                if isinstance(expected_value, dict):
                    expected_text = html_to_text(expected_value.get('value', ''))

            steps.append({
                'step': step_text,
                'expected_result': expected_text
            })

    return steps


def html_to_text(html: str) -> str:
    """Convert HTML to plain text, preserving structure"""
    if not html:
        return ''

    text = html

    # Preserve preformatted blocks (code, commands)
    # Replace <pre> and <code> with placeholders to preserve content
    text = re.sub(r'<pre[^>]*>(.*?)</pre>', r'\n\1\n', text, flags=re.IGNORECASE | re.DOTALL)
    text = re.sub(r'<code[^>]*>(.*?)</code>', r'\1', text, flags=re.IGNORECASE | re.DOTALL)

    # Convert paragraphs and divs to double newlines
    text = re.sub(r'</?p[^>]*>', '\n\n', text, flags=re.IGNORECASE)
    text = re.sub(r'</?div[^>]*>', '\n', text, flags=re.IGNORECASE)

    # Convert lists
    text = re.sub(r'<li[^>]*>', '\n- ', text, flags=re.IGNORECASE)
    text = re.sub(r'</li>', '', text, flags=re.IGNORECASE)
    text = re.sub(r'</?[ou]l[^>]*>', '\n', text, flags=re.IGNORECASE)

    # Convert line breaks
    text = re.sub(r'<br\s*/?>', '\n', text, flags=re.IGNORECASE)

    # Remove remaining HTML tags
    text = re.sub(r'<[^>]+>', '', text)

    # Decode HTML entities
    text = text.replace('&amp;', '&')
    text = text.replace('&lt;', '<')
    text = text.replace('&gt;', '>')
    text = text.replace('&quot;', '"')
    text = text.replace('&#39;', "'")
    text = text.replace('&nbsp;', ' ')

    # Clean up excessive newlines (max 2 consecutive)
    text = re.sub(r'\n{3,}', '\n\n', text)

    return text.strip()


def extract_custom_fields(attrs: Dict) -> Dict[str, Any]:
    """Extract custom fields from work item attributes"""
    custom_fields = {}

    # Map of custom field keys to friendly names
    field_map = {
        'casecomponent': 'component',
        'subteam2': 'sub_team',
        'mcoproducts': 'products',
        'casetesttype': 'test_type',
        'plannedin': 'version',
        'linkedworkitems': 'trello_jira',
    }

    for polarion_key, friendly_key in field_map.items():
        value = attrs.get(polarion_key)
        if value:
            # Handle different value types
            if isinstance(value, dict):
                # EnumOption or similar
                custom_fields[friendly_key] = value.get('id') or value.get('label', '')
            elif isinstance(value, list):
                # Special handling for linkedworkitems (extract work item IDs)
                if polarion_key == 'linkedworkitems':
                    # Extract work item keys/IDs from array of work item objects
                    work_items = []
                    for v in value:
                        if isinstance(v, dict):
                            # Try to get work item ID/key
                            item_id = v.get('id') or v.get('workItemId') or v.get('key', '')
                            if item_id:
                                work_items.append(item_id)
                        elif isinstance(v, str):
                            work_items.append(v)
                    # Join multiple work items with comma, or return first one
                    custom_fields[friendly_key] = ', '.join(work_items) if work_items else ''
                elif polarion_key == 'plannedin':
                    # Version field - take first version if array
                    versions = []
                    for v in value:
                        if isinstance(v, dict):
                            versions.append(v.get('id') or v.get('label', ''))
                        else:
                            versions.append(str(v))
                    custom_fields[friendly_key] = versions[0] if versions else ''
                else:
                    # Other arrays - extract IDs/labels
                    custom_fields[friendly_key] = [
                        v.get('id', v) if isinstance(v, dict) else v
                        for v in value
                    ]
            else:
                custom_fields[friendly_key] = value

    return custom_fields


def format_for_automate_test(tc_data: Dict) -> str:
    """Format test case for /automate-test command"""
    lines = []

    # Title and metadata
    lines.append(f"Test Case: {tc_data['id']}")
    lines.append(f"Title: {tc_data['title']}")
    lines.append("")

    # Description
    if tc_data.get('description'):
        lines.append("Description:")
        lines.append(tc_data['description'])
        lines.append("")

    # Custom fields
    fields = tc_data.get('custom_fields', {})
    if fields:
        lines.append("Metadata:")
        for key, value in fields.items():
            if value:
                lines.append(f"  {key}: {value}")
        lines.append("")

    # Test steps
    steps = tc_data.get('test_steps', [])
    if steps:
        lines.append("Test Steps:")
        lines.append("")
        for i, step in enumerate(steps, 1):
            lines.append(f"Step {i}:")
            lines.append(step['step'])
            lines.append("")
            lines.append("Expected Result:")
            lines.append(step['expected_result'])
            lines.append("")
            lines.append("-" * 80)
            lines.append("")

    return '\n'.join(lines)


def fetch_test_case(tc_id: str, project: str = None) -> Dict:
    """Fetch test case from Polarion"""
    env = load_env()

    polarion_url = env.get('POLARION_URL', 'https://polarion.engineering.redhat.com')
    polarion_token = env.get('POLARION_TOKEN', '')

    if not polarion_token or polarion_token == 'your_personal_access_token_here':
        print("Error: POLARION_TOKEN not configured in .env", file=sys.stderr)
        print("Generate token at: https://polarion.engineering.redhat.com/polarion/#/user/me", file=sys.stderr)
        sys.exit(1)

    # Parse project from TC ID if not provided
    if not project:
        project, tc_id = parse_test_case_id(tc_id)

    # Create client
    client = PolarionClient(
        url=polarion_url,
        token=polarion_token,
        verify_ssl=True
    )

    # Fetch test case with steps
    result = client.get_test_case(tc_id, project, include_test_steps=True)

    if result['status'] == 'failed':
        print(f"Error fetching test case: {result['error']}", file=sys.stderr)
        sys.exit(1)

    data = result['data']
    work_item = data.get('data', {})
    attrs = work_item.get('attributes', {})

    # Extract basic fields
    tc_data = {
        'id': tc_id,
        'project': project,
        'title': attrs.get('title', ''),
        'description': html_to_text(attrs.get('description', {}).get('value', '')),
        'status': attrs.get('status', ''),
        'type': attrs.get('type', ''),
    }

    # Extract test steps from REST API
    tc_data['test_steps'] = extract_test_steps(data)

    # If no test steps from REST API, try SOAP API as fallback
    if not tc_data['test_steps']:
        soap_result = client._soap_get_test_steps(tc_id, project)
        if soap_result['status'] == 'success':
            tc_data['test_steps'] = soap_result['steps']

    # Fetch custom fields (need separate call for full attributes)
    custom_result = client.get_workitem_custom_fields(tc_id, project)
    if custom_result['status'] == 'success':
        custom_data = custom_result['data']
        custom_attrs = custom_data.get('data', {}).get('attributes', {})
        tc_data['custom_fields'] = extract_custom_fields(custom_attrs)
    else:
        tc_data['custom_fields'] = {}

    return tc_data


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 fetch_polarion.py <test-case-id> [--project <project>]")
        print()
        print("Examples:")
        print("  python3 fetch_polarion.py OCP-88122")
        print("  python3 fetch_polarion.py OSE-12345 --project OSE")
        sys.exit(1)

    tc_id = sys.argv[1]
    project = None

    # Parse optional --project flag
    if '--project' in sys.argv:
        idx = sys.argv.index('--project')
        if idx + 1 < len(sys.argv):
            project = sys.argv[idx + 1]

    # Fetch test case
    tc_data = fetch_test_case(tc_id, project)

    # Output JSON to stdout
    print(json.dumps(tc_data, indent=2))

    # Write text format for /automate-test
    output_dir = '/tmp/test-specs'
    os.makedirs(output_dir, exist_ok=True)

    txt_file = os.path.join(output_dir, f"{tc_id}.txt")
    txt_content = format_for_automate_test(tc_data)

    with open(txt_file, 'w') as f:
        f.write(txt_content)

    print(f"\n✓ Test case saved to: {txt_file}", file=sys.stderr)


if __name__ == '__main__':
    main()
