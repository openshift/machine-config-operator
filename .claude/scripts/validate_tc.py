#!/usr/bin/env python3
"""
Validate Polarion test case draft JSON before creation.

Usage:
    python3 validate_tc.py <draft.json>
    python3 validate_tc.py <draft.json> --strict

Validates:
- Title format: ^[MCO][.+] .+
- Required fields: component, sub_team, products, test_type, version, trello_jira
- Test steps have real commands in expected_result (not narrative-only)
"""

import json
import re
import sys
from typing import Dict, List, Any, Tuple


class TCValidator:
    """Polarion test case validator"""

    # Required fields that must not be empty
    REQUIRED_FIELDS = [
        'component',
        'sub_team',
        'products',
        'test_type',
        'version',
        'trello_jira'
    ]

    # Title pattern: [MCO][TICKET-ID] Description
    TITLE_PATTERN = r'^\[MCO\]\[.+\] .+'

    # Patterns that indicate real commands (not just narrative)
    COMMAND_PATTERNS = [
        r'\$ ',                    # Shell prompt
        r'oc (get|create|apply|patch|delete|logs|debug|label|describe)',
        r'kubectl ',
        r'curl ',
        r'ssh ',
        r'oc apply -f ',
        r'apiVersion:',            # YAML content
        r'kind:',
        r'metadata:',
        r'Starting pod/',          # oc debug output
        r'State: idle',            # rpm-ostree status
        r'Deployments:',
        r'Version: \d+\.',         # Version output
        r'HTTP/\d',                # HTTP response
        r'machineconfigpool\.',    # Resource creation output
    ]

    def __init__(self, strict: bool = False):
        self.strict = strict
        self.errors = []
        self.warnings = []

    def validate_file(self, filepath: str) -> bool:
        """Validate TC from JSON file"""
        try:
            with open(filepath, 'r') as f:
                data = json.load(f)
        except FileNotFoundError:
            self.errors.append(f"File not found: {filepath}")
            return False
        except json.JSONDecodeError as e:
            self.errors.append(f"Invalid JSON: {e}")
            return False

        return self.validate(data)

    def validate(self, data: Dict[str, Any]) -> bool:
        """Validate TC data"""
        self.errors = []
        self.warnings = []

        # Validate title
        self._validate_title(data.get('title', ''))

        # Validate required fields
        self._validate_required_fields(data)

        # Validate test steps
        steps = data.get('test_steps', [])
        if steps:
            self._validate_test_steps(steps)
        else:
            self.warnings.append("No test steps defined")

        return len(self.errors) == 0 and (not self.strict or len(self.warnings) == 0)

    def _validate_title(self, title: str):
        """Validate title format"""
        if not title:
            self.errors.append("Title is empty")
            return

        if not re.match(self.TITLE_PATTERN, title):
            self.errors.append(
                f"Title does not match pattern: {self.TITLE_PATTERN}\n"
                f"  Got: {title}\n"
                f"  Expected: [MCO][TICKET-ID] Description\n"
                f"  Example: [MCO][MCO-2136] Validate osImageStream inheritance"
            )

    def _validate_required_fields(self, data: Dict[str, Any]):
        """Validate required fields are present and non-empty"""
        for field in self.REQUIRED_FIELDS:
            value = data.get(field, '')

            if not value or (isinstance(value, str) and value.strip() == ''):
                self.errors.append(f"Required field '{field}' is empty")
            elif isinstance(value, str):
                # Check for placeholder values
                if value.lower() in ['todo', 'tbd', 'fixme', 'xxx']:
                    self.errors.append(
                        f"Required field '{field}' has placeholder value: {value}"
                    )

    def _validate_test_steps(self, steps: List[Dict[str, str]]):
        """Validate test steps have real commands, not just narrative"""
        for i, step in enumerate(steps, 1):
            step_text = step.get('step', '')
            expected = step.get('expected_result', '')

            if not step_text:
                self.warnings.append(f"Step {i}: 'step' field is empty")

            if not expected:
                self.errors.append(f"Step {i}: 'expected_result' field is empty")
                continue

            # Reject heredoc syntax — causes Polarion rendering breakage
            if re.search(r'<<\s*EOF\b', expected):
                self.errors.append(
                    f"Step {i}: heredoc syntax (<<EOF) detected in expected_result. "
                    "Replace with file-based YAML + oc apply -f filename.yaml pattern."
                )

            # Check if expected result has actual commands
            has_command = any(
                re.search(pattern, expected, re.IGNORECASE)
                for pattern in self.COMMAND_PATTERNS
            )

            if not has_command:
                # Check if it's purely narrative (long sentences, no technical content)
                is_narrative = self._is_narrative_only(expected)

                if is_narrative:
                    msg = (
                        f"Step {i}: Expected result appears to be narrative-only.\n"
                        f"  Include actual commands and expected output.\n"
                        f"  Got: {expected[:100]}..."
                    )
                    if self.strict:
                        self.errors.append(msg)
                    else:
                        self.warnings.append(msg)

    def _is_narrative_only(self, text: str) -> bool:
        """Check if text is narrative-only (no commands or technical output)"""
        # Narrative indicators
        narrative_words = [
            'should', 'will', 'must', 'verify', 'ensure', 'check',
            'confirms', 'validates', 'processes', 'triggers', 'detects'
        ]

        # Count narrative words
        narrative_count = sum(
            1 for word in narrative_words
            if word in text.lower()
        )

        # If mostly narrative and no technical patterns, it's narrative-only
        if narrative_count >= 2:
            has_technical = any(
                pattern in text
                for pattern in ['$', 'oc ', 'kubectl', 'apiVersion', ':', '{', '}']
            )
            return not has_technical

        return False

    def print_report(self):
        """Print validation report"""
        if self.errors:
            print("❌ VALIDATION FAILED\n")
            print("Errors:")
            for i, err in enumerate(self.errors, 1):
                print(f"  {i}. {err}")
            print()

        if self.warnings:
            print("⚠️  Warnings:")
            for i, warn in enumerate(self.warnings, 1):
                print(f"  {i}. {warn}")
            print()

        if not self.errors and not self.warnings:
            print("✓ VALIDATION PASSED")
            print("  All checks passed. TC is ready for creation.")
        elif not self.errors:
            print("✓ VALIDATION PASSED (with warnings)")
            print("  TC meets minimum requirements but has warnings.")

    def get_exit_code(self) -> int:
        """Get exit code based on validation result"""
        return 1 if self.errors or (self.strict and self.warnings) else 0


def create_sample_draft() -> str:
    """Create a sample draft for testing"""
    sample = {
        "title": "[MCO][MCO-2136] Validate osImageStream inheritance for custom MachineConfigPools",
        "description": "Verify that custom MCPs inherit osImageStream from worker pool when not explicitly set",
        "component": "Machine Config Operator",
        "sub_team": "MCO",
        "products": "OCP",
        "test_type": "Functional",
        "version": "4.22",
        "trello_jira": "MCO-2136",
        "test_steps": [
            {
                "step": "Create custom MCP without osImageStream field",
                "expected_result": (
                    "# rhel9-mcp.yaml\n"
                    "apiVersion: machineconfiguration.openshift.io/v1\n"
                    "kind: MachineConfigPool\n"
                    "metadata:\n"
                    "  name: rhel9\n\n"
                    "$ oc apply -f rhel9-mcp.yaml\n"
                    "machineconfigpool.machineconfiguration.openshift.io/rhel9 created"
                )
            },
            {
                "step": "Check osImageStream is inherited",
                "expected_result": (
                    "$ oc get mcp rhel9 -ojsonpath='{.status.osImageStream.name}'\n"
                    "rhel9"
                )
            }
        ]
    }

    filepath = '/tmp/test-specs/sample_draft.json'
    with open(filepath, 'w') as f:
        json.dump(sample, f, indent=2)

    return filepath


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 validate_tc.py <draft.json> [--strict]")
        print()
        print("Options:")
        print("  --strict    Treat warnings as errors")
        print()
        print("To create a sample draft:")
        print("  python3 validate_tc.py --sample")
        sys.exit(1)

    # Handle --sample flag
    if sys.argv[1] == '--sample':
        filepath = create_sample_draft()
        print(f"✓ Sample draft created at: {filepath}")
        print()
        print("Validating sample...")
        print()
        validator = TCValidator(strict=False)
        validator.validate_file(filepath)
        validator.print_report()
        sys.exit(0)

    filepath = sys.argv[1]
    strict = '--strict' in sys.argv

    print(f"Validating: {filepath}")
    if strict:
        print("Mode: STRICT (warnings treated as errors)")
    print()

    validator = TCValidator(strict=strict)
    validator.validate_file(filepath)
    validator.print_report()

    sys.exit(validator.get_exit_code())


if __name__ == '__main__':
    main()
