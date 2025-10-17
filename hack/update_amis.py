#!/usr/bin/env python3
"""
Script to update AMI list in ami.go from the OpenShift installer repository.

This script:
1. Fetches the rhcos.json file from the installer repo
2. Steps back through git history from HEAD to the last recorded commit
3. Extracts new AMI IDs from each commit
4. Validates that no duplicate AMIs are being added
5. Updates ami.go with new AMIs and the latest commit hash
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


def get_current_commit_hash(ami_go_path: Path) -> str:
    """Extract the current source commit hash from ami.go."""
    with open(ami_go_path, 'r') as f:
        for line in f:
            if 'source commit hash' in line:
                match = re.search(r'= ([a-f0-9]+)', line)
                if match:
                    return match.group(1)
    raise ValueError("Could not find source commit hash in ami.go")


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


def get_commits_between(repo_path: Path, start_commit: str, file_path: str) -> List[str]:
    """Get list of commits from start_commit to HEAD for the given file."""
    # Get commits from start_commit (exclusive) to HEAD
    cmd = ['git', 'log', '--pretty=format:%H', f'{start_commit}..HEAD', '--', file_path]
    output = run_command(cmd, cwd=str(repo_path), check=False)

    if not output:
        log_warn(f"No commits found between {start_commit} and HEAD")
        # Try getting recent commits
        cmd = ['git', 'log', '--pretty=format:%H', '-n', '10', '--', file_path]
        output = run_command(cmd, cwd=str(repo_path), check=False)

    commits = [line.strip() for line in output.split('\n') if line.strip()]
    # Reverse to process oldest first
    return list(reversed(commits))


def get_file_at_commit(repo_path: Path, commit: str, file_path: str) -> str:
    """Get the content of a file at a specific commit."""
    cmd = ['git', 'show', f'{commit}:{file_path}']
    return run_command(cmd, cwd=str(repo_path), check=False)


def update_ami_go_file(ami_go_path: Path, new_amis: Set[str], existing_amis: List[str],
                       latest_commit: str) -> bool:
    """Update the ami.go file with new AMIs and commit hash."""
    # Keep existing AMIs in order, then add new AMIs sorted at the end
    new_amis_sorted = sorted(new_amis)
    all_amis = existing_amis + new_amis_sorted

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
    for i in range(0, len(all_amis), amis_per_line):
        chunk = all_amis[i:i+amis_per_line]
        formatted = ', '.join(f'"{ami}"' for ami in chunk)
        # Always add comma at the end
        formatted += ','
        formatted_lines.append(f'\t{formatted}\n')

    # Update commit hash in the lines before the AMI section
    for i in range(start_idx):
        if 'source commit hash' in lines[i]:
            lines[i] = re.sub(
                r'(source commit hash = )[a-f0-9]+',
                f'\\g<1>{latest_commit}',
                lines[i]
            )

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

    # Get current commit hash from ami.go
    current_commit = get_current_commit_hash(AMI_GO_FILE)
    log_info(f"Current source commit in ami.go: {current_commit}")

    # Get existing AMIs
    existing_amis = get_existing_amis(AMI_GO_FILE)
    existing_amis_set = set(existing_amis)
    log_info(f"Found {len(existing_amis)} existing AMI IDs in ami.go")

    # Create temporary directory and clone with filter
    with tempfile.TemporaryDirectory() as temp_dir:
        repo_path = Path(temp_dir)

        log_info(f"Cloning repository with filter for {FILE_PATH}...")

        # Clone with --filter=blob:none --no-checkout for faster cloning
        run_command([
            'git', 'clone', '--quiet', '--filter=blob:none', '--no-checkout',
            '--depth=100', '--single-branch', '--branch=main',
            REPO_URL, str(repo_path)
        ])

        # Checkout only the specific file
        run_command(['git', 'checkout', 'main', '--', FILE_PATH], cwd=str(repo_path))

        # Check if current commit is up to date with remote
        log_info("Checking if file is up to date...")
        latest_remote_commit = run_command(
            ['git', 'log', '-n', '1', '--pretty=format:%H', '--', FILE_PATH],
            cwd=str(repo_path)
        )

        if latest_remote_commit == current_commit:
            log_info("File is already up to date! No new commits to process.")
            sys.exit(0)

        # Get commits to process
        log_info(f"Getting commit history from {current_commit[:8]} to latest...")
        commits = get_commits_between(repo_path, current_commit, FILE_PATH)

        if not commits:
            log_warn("No new commits found")
            sys.exit(0)

        log_info(f"Found {len(commits)} commit(s) to process")

        # Track new AMIs and their source commits
        new_amis_map: Dict[str, str] = {}
        latest_commit = None

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

            if not commit_amis:
                log_warn(f"No AMIs found in commit {commit[:8]}")
                continue

            log_info(f"  Found {len(commit_amis)} AMI(s) in this commit")
            latest_commit = commit

            # Check each AMI
            for ami in commit_amis:
                # Check if AMI already exists in ami.go
                if ami in existing_amis_set:
                    log_error(f"Duplicate AMI detected: {ami}")
                    log_error(f"  This AMI already exists in ami.go")
                    log_error(f"  Found in commit: {commit[:8]}")
                    sys.exit(1)

                # Check if AMI was already added by a previous commit in this run
                if ami in new_amis_map:
                    log_error(f"Duplicate AMI detected: {ami}")
                    log_error(f"  First seen in commit: {new_amis_map[ami][:8]}")
                    log_error(f"  Duplicate in commit: {commit[:8]}")
                    sys.exit(1)

                # Add to new AMIs map
                new_amis_map[ami] = commit

        # Check if we found any new AMIs
        if not new_amis_map:
            log_warn("No new AMIs found")
            sys.exit(0)

        log_info(f"Found {len(new_amis_map)} new AMI(s) to add")

        if not latest_commit:
            log_error("No valid commits were processed")
            sys.exit(1)

        log_info(f"Latest processed commit: {latest_commit[:8]}")

    # Update ami.go file
    log_info(f"Updating {AMI_GO_FILE}...")

    success = update_ami_go_file(
        AMI_GO_FILE,
        set(new_amis_map.keys()),
        existing_amis,
        latest_commit
    )

    if not success:
        log_error("Failed to update ami.go")
        sys.exit(1)

    # Print summary
    log_info("Update complete!")
    log_info(f"  - Updated source commit hash to: {latest_commit[:8]}")
    log_info(f"  - Added {len(new_amis_map)} new AMI(s)")
    log_info(f"  - Total AMI count: {len(existing_amis) + len(new_amis_map)}")

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
