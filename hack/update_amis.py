#!/usr/bin/env python3
"""
Script to update AMI list in ami.go from the OpenShift installer repository.

This script:
1. Fetches the rhcos.json file from the installer repo
2. Walks through git history of release-4.12+ branches and main
3. Extracts all AMI IDs from all commits across these branches
4. Adds any new AMIs to ami.go (never deletes existing AMIs)
"""

import os
import re
import sys
import json
import tempfile
import subprocess
from typing import Set, Dict, List, Tuple
from pathlib import Path


class Color:
    """ANSI color codes for terminal output."""
    RED = '\033[0;31m'
    GREEN = '\033[0;32m'
    YELLOW = '\033[1;33m'
    BLUE = '\033[0;34m'
    NC = '\033[0m'  # No Color


def log_info(message: str):
    """Print info message in green."""
    print(f"{Color.GREEN}[INFO]{Color.NC} {message}")


def log_warn(message: str):
    """Print warning message in yellow."""
    print(f"{Color.YELLOW}[WARN]{Color.NC} {message}")


def log_error(message: str):
    """Print error message in red."""
    print(f"{Color.RED}[ERROR]{Color.NC} {message}", file=sys.stderr)


def run_command(cmd: List[str], cwd: str = None, check: bool = True) -> str:
    """Run a shell command and return its output."""
    try:
        result = subprocess.run(
            cmd,
            cwd=cwd,
            capture_output=True,
            text=True,
            check=check
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        if check:
            log_error(f"Command failed: {' '.join(cmd)}")
            log_error(f"Error: {e.stderr}")
            raise
        return ""


def get_existing_amis(ami_go_path: Path) -> List[str]:
    """Extract all existing AMI IDs from ami.go in order."""
    amis = []
    with open(ami_go_path, 'r') as f:
        in_ami_section = False
        for line in f:
            if 'AllowedAMIs = sets.New(' in line:
                in_ami_section = True
                continue
            if in_ami_section:
                if line.strip() == ')':
                    break
                # Extract AMI IDs from the line
                ami_matches = re.findall(r'"(ami-[a-f0-9]+)"', line)
                amis.extend(ami_matches)
    return amis


def extract_amis_from_json(json_content: str) -> Set[str]:
    """Extract AMI IDs from rhcos.json content."""
    amis = set()
    try:
        data = json.loads(json_content)
        # Navigate through the JSON structure to find AMI IDs
        if 'amis' in data:
            for arch_data in data['amis']:
                if 'hvm' in arch_data:
                    ami = arch_data['hvm']
                    if ami.startswith('ami-'):
                        amis.add(ami)

        # Also search for AMI patterns in the entire JSON
        ami_matches = re.findall(r'ami-[a-f0-9]{8,}', json_content)
        amis.update(ami_matches)

    except json.JSONDecodeError as e:
        log_warn(f"Failed to parse JSON: {e}")
        # Fall back to regex extraction
        ami_matches = re.findall(r'ami-[a-f0-9]{8,}', json_content)
        amis.update(ami_matches)

    return amis


def get_relevant_branches(repo_path: Path) -> List[str]:
    """Get list of branches to process (release-4.12 onwards and main)."""
    cmd = ['git', 'branch', '-r']
    output = run_command(cmd, cwd=str(repo_path), check=False)

    branches = []
    for line in output.split('\n'):
        line = line.strip()
        if not line:
            continue

        # Remove 'origin/' prefix
        branch = line.replace('origin/', '')

        # Include main branch
        if branch == 'main':
            branches.append(branch)
            continue

        # Include release-4.X branches where X >= 12
        match = re.match(r'release-4\.(\d+)$', branch)
        if match:
            version = int(match.group(1))
            if version >= 12:
                branches.append(branch)

    return sorted(branches)


def get_all_commits_from_branches(repo_path: Path, branches: List[str], file_path: str) -> List[str]:
    """Get list of all commits for the given file across specified branches."""
    all_commits = set()

    for branch in branches:
        cmd = ['git', 'log', f'origin/{branch}', '--pretty=format:%H', '--', file_path]
        output = run_command(cmd, cwd=str(repo_path), check=False)

        if output:
            commits = [line.strip() for line in output.split('\n') if line.strip()]
            all_commits.update(commits)

    # Convert to list and sort by commit (we'll process them all anyway)
    return list(all_commits)


def get_file_at_commit(repo_path: Path, commit: str, file_path: str) -> str:
    """Get the content of a file at a specific commit."""
    cmd = ['git', 'show', f'{commit}:{file_path}']
    return run_command(cmd, cwd=str(repo_path), check=False)


def update_ami_go_file(ami_go_path: Path, existing_amis: List[str], new_amis: Set[str]) -> bool:
    """Update the ami.go file with existing AMIs plus any new AMIs found."""
    # Keep existing AMIs in order, add new AMIs sorted at the end.
    # We deliberately do NOT fully sort the entire list to keep git diffs clean -
    # this way diffs only show the newly added AMIs rather than reshuffling all 8000+ entries.
    # The tradeoff is that the file becomes a series of sorted chunks over time rather than
    # being fully sorted, but the improved reviewability is worth it.
    new_amis_sorted = sorted(new_amis)
    all_amis_sorted = existing_amis + new_amis_sorted

    # Read the original file
    with open(ami_go_path, 'r') as f:
        lines = f.readlines()

    # Find the AMI section
    start_idx = None
    end_idx = None
    for i, line in enumerate(lines):
        if 'AllowedAMIs = sets.New(' in line:
            start_idx = i
        elif start_idx is not None and line.strip() == ')':
            end_idx = i
            break

    if start_idx is None or end_idx is None:
        log_error("Could not find AllowedAMIs section in ami.go")
        return False

    # Always use 5 AMIs per line
    amis_per_line = 5

    # Format AMIs for Go code (5 per line)
    formatted_lines = []
    for i in range(0, len(all_amis_sorted), amis_per_line):
        chunk = all_amis_sorted[i:i+amis_per_line]
        formatted = ', '.join(f'"{ami}"' for ami in chunk)
        # Always add comma at the end
        formatted += ','
        formatted_lines.append(f'\t{formatted}\n')

    # Reconstruct the file
    new_lines = (
        lines[:start_idx+1] +  # Everything up to and including "AllowedAMIs = sets.New("
        formatted_lines +       # The formatted AMI list
        [lines[end_idx]] +      # The closing ")"
        lines[end_idx+1:]       # Everything after
    )

    # Write back to file
    with open(ami_go_path, 'w') as f:
        f.writelines(new_lines)

    return True


def main():
    """Main function to update AMIs."""
    # Configuration
    REPO_URL = "https://github.com/openshift/installer"
    FILE_PATH = "data/data/coreos/rhcos.json"

    # Determine the project root (parent of hack directory)
    script_dir = Path(__file__).parent.resolve()
    project_root = script_dir.parent
    AMI_GO_FILE = project_root / "pkg/controller/machine-set-boot-image/ami.go"

    # Check if ami.go exists
    if not AMI_GO_FILE.exists():
        log_error(f"File not found: {AMI_GO_FILE}")
        sys.exit(1)

    log_info("Starting AMI update process...")

    # Get existing AMIs from ami.go
    existing_amis = get_existing_amis(AMI_GO_FILE)
    existing_amis_set = set(existing_amis)
    log_info(f"Found {len(existing_amis)} existing AMI IDs in ami.go")

    log_info("Walking through entire git history to collect all AMIs...")

    # Create temporary directory and clone with filter
    with tempfile.TemporaryDirectory() as temp_dir:
        repo_path = Path(temp_dir)

        log_info(f"Cloning repository with full history for {FILE_PATH}...")

        # Clone with --filter=blob:none --no-checkout for faster cloning
        # Don't use --single-branch since we need multiple branches
        run_command([
            'git', 'clone', '--quiet', '--filter=blob:none', '--no-checkout',
            REPO_URL, str(repo_path)
        ])

        # Get relevant branches (release-4.12 onwards and main)
        log_info("Finding relevant branches...")
        branches = get_relevant_branches(repo_path)

        if not branches:
            log_error("No relevant branches found")
            sys.exit(1)

        log_info(f"Found {len(branches)} branches to process: {', '.join(branches)}")

        # Get all commits from these branches
        log_info("Getting commit history from all branches...")
        commits = get_all_commits_from_branches(repo_path, branches, FILE_PATH)

        if not commits:
            log_error("No commits found for file")
            sys.exit(1)

        log_info(f"Found {len(commits)} commit(s) to process")

        # Track all AMIs found across all commits
        all_amis_from_history: Set[str] = set()

        # Process each commit
        for commit in commits:
            log_info(f"Processing commit: {commit[:8]}...")

            # Get file content at this commit
            content = get_file_at_commit(repo_path, commit, FILE_PATH)

            if not content:
                log_warn(f"Could not retrieve {FILE_PATH} at commit {commit[:8]}, skipping...")
                continue

            # Extract AMIs from this version
            commit_amis = extract_amis_from_json(content)

            if commit_amis:
                log_info(f"  Found {len(commit_amis)} AMI(s) in this commit")
                all_amis_from_history.update(commit_amis)

        # Check if we found any AMIs
        if not all_amis_from_history:
            log_error("No AMIs found in git history")
            sys.exit(1)

        log_info(f"Collected {len(all_amis_from_history)} unique AMI(s) from git history")

        # Find new AMIs (not in existing set)
        new_amis = all_amis_from_history - existing_amis_set

        if not new_amis:
            log_info("No new AMIs to add. ami.go is up to date.")
            sys.exit(0)

        log_info(f"Found {len(new_amis)} new AMI(s) to add")

    # Update ami.go file
    log_info(f"Updating {AMI_GO_FILE}...")

    success = update_ami_go_file(
        AMI_GO_FILE,
        existing_amis,
        new_amis
    )

    if not success:
        log_error("Failed to update ami.go")
        sys.exit(1)

    # Print summary
    log_info("Update complete!")
    log_info(f"  - Added {len(new_amis)} new AMI(s)")
    log_info(f"  - Total AMI count: {len(existing_amis) + len(new_amis)}")

if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        log_warn("\nInterrupted by user")
        sys.exit(1)
    except Exception as e:
        log_error(f"Unexpected error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
