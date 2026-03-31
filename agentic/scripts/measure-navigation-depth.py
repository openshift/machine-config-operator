#!/usr/bin/env python3
"""
Measure navigation depth from AGENTS.md to all documentation.

Metrics:
- Maximum hop count from AGENTS.md
- Average hop count
- Unreachable documents
- Per-document depth distribution

Usage:
    ./scripts/measure-navigation-depth.py
    ./scripts/measure-navigation-depth.py --max-depth 3 --fail-on-violation
"""

import re
import os
import sys
import argparse
from pathlib import Path
from collections import defaultdict, deque
from typing import Dict, Set, List, Tuple


def extract_markdown_links(file_path: Path, base_dir: Path) -> Set[Path]:
    """Extract all relative markdown links from a file."""
    links = set()

    try:
        with open(file_path, 'r', encoding='utf-8') as f:
            content = f.read()

        # Match markdown links: [text](path)
        # Also match bare links without brackets
        link_pattern = r'\[([^\]]+)\]\(([^\)]+)\)|(?:^|\s)((?:\.{1,2}/|\./)[^\s\)]+\.md)'

        for match in re.finditer(link_pattern, content, re.MULTILINE):
            link = match.group(2) if match.group(2) else match.group(3)

            if not link:
                continue

            # Skip external links, anchors
            if link.startswith(('http://', 'https://', '#', 'mailto:')):
                continue

            # Remove anchor fragments
            link = link.split('#')[0]

            if not link or not link.endswith('.md'):
                continue

            # Resolve relative path
            link_path = (file_path.parent / link).resolve()

            # Only include if it's within our repo
            try:
                link_path.relative_to(base_dir)
                if link_path.exists():
                    links.add(link_path)
            except ValueError:
                # Link points outside repo
                pass

    except Exception as e:
        print(f"Warning: Could not parse {file_path}: {e}", file=sys.stderr)

    return links


def build_link_graph(base_dir: Path, entry_point: Path) -> Dict[Path, Set[Path]]:
    """Build a directed graph of markdown links."""
    graph = defaultdict(set)
    visited = set()
    to_visit = {entry_point}

    while to_visit:
        current = to_visit.pop()
        if current in visited:
            continue

        visited.add(current)
        links = extract_markdown_links(current, base_dir)
        graph[current] = links

        # Add newly discovered nodes to visit
        to_visit.update(links - visited)

    return dict(graph)


def calculate_depths(graph: Dict[Path, Set[Path]], entry_point: Path) -> Dict[Path, int]:
    """Calculate shortest path distance from entry_point to all nodes using BFS."""
    depths = {entry_point: 0}
    queue = deque([entry_point])

    while queue:
        current = queue.popleft()
        current_depth = depths[current]

        for neighbor in graph.get(current, set()):
            if neighbor not in depths:
                depths[neighbor] = current_depth + 1
                queue.append(neighbor)

    return depths


def find_all_docs(base_dir: Path, patterns: List[str]) -> Set[Path]:
    """Find all documentation files that should be reachable."""
    docs = set()

    for pattern in patterns:
        docs.update(base_dir.glob(pattern))

    return docs


def analyze_navigation(base_dir: Path, entry_point: Path, max_depth: int = 3) -> Dict:
    """Analyze navigation structure and return metrics."""

    # Build the link graph
    print("Building link graph...")
    graph = build_link_graph(base_dir, entry_point)

    # Calculate depths
    print("Calculating navigation depths...")
    depths = calculate_depths(graph, entry_point)

    # Find all docs that should be reachable (from patterns)
    expected_docs = find_all_docs(base_dir, [
        'agentic/**/*.md',
        'AGENTS.md',
        'ARCHITECTURE.md',
        'CONTRIBUTING.md',
        'README.md'
    ])

    # Classify documents
    reachable_all = set(depths.keys())  # All files discovered by graph traversal

    # Calculate different categories
    reachable_expected = expected_docs & reachable_all  # Expected AND reachable
    unreachable = expected_docs - reachable_all  # Expected but NOT reachable
    discovered_only = reachable_all - expected_docs  # Reachable but NOT expected (like docs/)

    # For reporting purposes
    all_docs = expected_docs  # What we're measuring
    reachable = reachable_expected  # Reachable docs from our expected set

    # Over limit = found but too deep
    over_limit = {doc: depth for doc, depth in depths.items() if depth > max_depth}

    # Calculate statistics
    if depths:
        max_observed_depth = max(depths.values())
        avg_depth = sum(depths.values()) / len(depths)
        depth_distribution = defaultdict(int)
        for depth in depths.values():
            depth_distribution[depth] += 1
    else:
        max_observed_depth = 0
        avg_depth = 0
        depth_distribution = {}

    return {
        'entry_point': entry_point,
        'max_depth_limit': max_depth,
        'max_observed_depth': max_observed_depth,
        'avg_depth': avg_depth,
        'total_docs': len(all_docs),
        'reachable_docs': len(reachable),
        'unreachable_docs': unreachable,
        'over_limit_docs': over_limit,
        'depth_distribution': dict(depth_distribution),
        'all_depths': depths
    }


def print_report(analysis: Dict, verbose: bool = False):
    """Print analysis report."""
    print("\n" + "="*70)
    print("NAVIGATION DEPTH ANALYSIS")
    print("="*70)
    print(f"Entry Point: {analysis['entry_point'].name}")
    print(f"Max Depth Limit: {analysis['max_depth_limit']} hops")
    print()

    print("SUMMARY")
    print("-"*70)
    print(f"  Total documents found:     {analysis['total_docs']}")
    print(f"  Reachable documents:       {analysis['reachable_docs']}")
    print(f"  Unreachable documents:     {len(analysis['unreachable_docs'])}")
    print(f"  Max observed depth:        {analysis['max_observed_depth']} hops")
    print(f"  Average depth:             {analysis['avg_depth']:.2f} hops")
    print(f"  Docs exceeding limit:      {len(analysis['over_limit_docs'])}")
    print()

    print("DEPTH DISTRIBUTION")
    print("-"*70)
    for depth in sorted(analysis['depth_distribution'].keys()):
        count = analysis['depth_distribution'][depth]
        bar = "█" * min(count, 50)
        print(f"  {depth} hops: {count:3d} docs {bar}")
    print()

    # Violations
    if analysis['over_limit_docs']:
        print(f"⚠️  DOCS EXCEEDING {analysis['max_depth_limit']} HOPS")
        print("-"*70)
        for doc, depth in sorted(analysis['over_limit_docs'].items(), key=lambda x: x[1], reverse=True):
            rel_path = doc.relative_to(Path.cwd())
            print(f"  {depth} hops: {rel_path}")
        print()

    if analysis['unreachable_docs']:
        print("❌ UNREACHABLE DOCUMENTS")
        print("-"*70)
        for doc in sorted(analysis['unreachable_docs']):
            rel_path = doc.relative_to(Path.cwd())
            print(f"  {rel_path}")
        print()

    # Pass/Fail
    print("RESULT")
    print("-"*70)

    issues = []
    if analysis['over_limit_docs']:
        issues.append(f"{len(analysis['over_limit_docs'])} docs exceed max depth")
    if analysis['unreachable_docs']:
        issues.append(f"{len(analysis['unreachable_docs'])} docs unreachable")

    if issues:
        print(f"❌ FAILED: {', '.join(issues)}")
        print()
        return False
    else:
        print(f"✅ PASSED: All docs reachable within {analysis['max_depth_limit']} hops")
        print()
        return True

    if verbose:
        print("\nALL DOCUMENT DEPTHS")
        print("-"*70)
        for doc, depth in sorted(analysis['all_depths'].items(), key=lambda x: x[1]):
            rel_path = doc.relative_to(Path.cwd())
            print(f"  {depth} hops: {rel_path}")


def main():
    parser = argparse.ArgumentParser(description='Measure navigation depth in agentic docs')
    parser.add_argument('--entry-point', default='AGENTS.md', help='Entry point file (default: AGENTS.md)')
    parser.add_argument('--max-depth', type=int, default=3, help='Maximum allowed hop count (default: 3)')
    parser.add_argument('--fail-on-violation', action='store_true', help='Exit with error code if violations found')
    parser.add_argument('--verbose', '-v', action='store_true', help='Show all document depths')

    args = parser.parse_args()

    base_dir = Path.cwd()
    entry_point = base_dir / args.entry_point

    if not entry_point.exists():
        print(f"Error: Entry point not found: {entry_point}", file=sys.stderr)
        sys.exit(1)

    analysis = analyze_navigation(base_dir, entry_point, args.max_depth)
    passed = print_report(analysis, verbose=args.verbose)

    if args.fail_on_violation and not passed:
        sys.exit(1)


if __name__ == '__main__':
    main()
