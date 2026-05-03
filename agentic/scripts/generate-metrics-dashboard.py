#!/usr/bin/env python3
"""
Generate HTML dashboard for agentic documentation metrics.

Usage:
    ./scripts/generate-metrics-dashboard.py
    ./scripts/generate-metrics-dashboard.py --output docs/metrics-dashboard.html
    ./scripts/generate-metrics-dashboard.py --open  # Generate and open in browser
"""

import argparse
import json
import subprocess
import sys
import webbrowser
from datetime import datetime
from pathlib import Path


def run_metric_script(script_path: Path, *args) -> dict:
    """Run a metric script and return parsed output."""
    try:
        result = subprocess.run(
            ['python3', str(script_path)] + list(args),
            capture_output=True,
            text=True,
            cwd=Path.cwd()
        )
        return {
            'success': result.returncode == 0,
            'output': result.stdout,
            'error': result.stderr
        }
    except Exception as e:
        return {
            'success': False,
            'output': '',
            'error': str(e)
        }


def parse_navigation_metrics(output: str) -> dict:
    """Parse navigation depth script output."""
    metrics = {
        'max_depth': 0,
        'avg_depth': 0.0,
        'total_docs': 0,
        'reachable_docs': 0,
        'unreachable_count': 0,
        'over_limit_count': 0,
        'discovered_beyond_expected': 0,
        'status': 'unknown'
    }

    for line in output.split('\n'):
        if 'Max observed depth:' in line:
            metrics['max_depth'] = int(line.split(':')[1].strip().split()[0])
        elif 'Average depth:' in line:
            metrics['avg_depth'] = float(line.split(':')[1].strip().split()[0])
        elif 'Total documents found:' in line:
            metrics['total_docs'] = int(line.split(':')[1].strip())
        elif 'Reachable documents:' in line:
            metrics['reachable_docs'] = int(line.split(':')[1].strip())
        elif 'Unreachable documents:' in line:
            metrics['unreachable_count'] = int(line.split(':')[1].strip())
        elif 'Docs exceeding limit:' in line:
            metrics['over_limit_count'] = int(line.split(':')[1].strip())
        elif 'PASSED' in line:
            metrics['status'] = 'pass'
        elif 'FAILED' in line:
            metrics['status'] = 'fail'

    return metrics


def parse_context_budget(output: str) -> dict:
    """Parse context budget script output."""
    metrics = {
        'workflows': [],
        'max_observed': 0,
        'avg_observed': 0,
        'passing': 0,
        'failing': 0,
        'status': 'unknown'
    }

    current_workflow = None
    for line in output.split('\n'):
        if line.strip() and not line.startswith(('=', '-', 'CONTEXT', 'Budget', 'Recommendations')):
            if 'Status:' in line:
                if current_workflow:
                    if '✅ OK' in line:
                        status = 'pass'
                        metrics['passing'] += 1
                    else:
                        status = 'fail'
                        metrics['failing'] += 1

                    # Extract line count
                    import re
                    match = re.search(r'\((\d+)/(\d+) lines', line)
                    if match:
                        current_workflow['actual'] = int(match.group(1))
                        current_workflow['limit'] = int(match.group(2))
                        current_workflow['status'] = status
                        metrics['workflows'].append(current_workflow)
                        current_workflow = None
            elif line[0].isupper() and not line.startswith(('SUMMARY', 'Workflows')):
                # New workflow
                parts = line.split('\n')
                current_workflow = {'name': parts[0].strip()}

        if 'Max observed:' in line:
            metrics['max_observed'] = int(line.split(':')[1].strip().split()[0])
        elif 'Average observed:' in line:
            metrics['avg_observed'] = int(line.split(':')[1].strip().split()[0])
        elif 'PASSED' in line:
            metrics['status'] = 'pass'
        elif 'FAILED' in line:
            metrics['status'] = 'fail'

    return metrics


def generate_html_dashboard(nav_metrics: dict, budget_metrics: dict, output_path: Path):
    """Generate HTML dashboard."""

    # Calculate individual scores
    nav_score = 100 if nav_metrics['status'] == 'pass' else 50
    budget_score = 100 if budget_metrics['status'] == 'pass' else 75
    structure_score = 100  # Always 100 if scripts run (checked earlier)
    coverage_score = 100  # Assume 100 for now (can be enhanced later)

    # Average all 4 metrics to match terminal output
    overall_score = (nav_score + budget_score + structure_score + coverage_score) // 4

    # Determine overall status
    if overall_score >= 90:
        overall_status = 'excellent'
        overall_label = 'EXCELLENT'
        overall_color = '#10b981'
    elif overall_score >= 80:
        overall_status = 'good'
        overall_label = 'GOOD'
        overall_color = '#3b82f6'
    elif overall_score >= 70:
        overall_status = 'fair'
        overall_label = 'FAIR'
        overall_color = '#f59e0b'
    else:
        overall_status = 'poor'
        overall_label = 'POOR'
        overall_color = '#ef4444'

    html = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentic Documentation Metrics Dashboard</title>
    <style>
        * {{
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }}

        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 2rem;
        }}

        .container {{
            max-width: 1200px;
            margin: 0 auto;
        }}

        .header {{
            text-align: center;
            color: white;
            margin-bottom: 2rem;
        }}

        .header h1 {{
            font-size: 2.5rem;
            margin-bottom: 0.5rem;
        }}

        .header p {{
            opacity: 0.9;
            font-size: 1.1rem;
        }}

        .dashboard {{
            background: white;
            border-radius: 1rem;
            box-shadow: 0 20px 60px rgba(0,0,0,0.3);
            overflow: hidden;
        }}

        .score-banner {{
            background: {overall_color};
            color: white;
            padding: 3rem 2rem;
            text-align: center;
        }}

        .score-circle {{
            width: 200px;
            height: 200px;
            border-radius: 50%;
            background: rgba(255,255,255,0.2);
            margin: 0 auto 1rem;
            display: flex;
            align-items: center;
            justify-content: center;
            flex-direction: column;
            border: 8px solid rgba(255,255,255,0.3);
        }}

        .score-number {{
            font-size: 4rem;
            font-weight: bold;
            line-height: 1;
        }}

        .score-label {{
            font-size: 1.5rem;
            margin-top: 0.5rem;
            opacity: 0.9;
        }}

        .metrics-grid {{
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 1.5rem;
            padding: 2rem;
        }}

        .metric-card {{
            background: #f8fafc;
            border-radius: 0.5rem;
            padding: 1.5rem;
            border-left: 4px solid #cbd5e1;
        }}

        .metric-card.pass {{
            border-left-color: #10b981;
        }}

        .metric-card.fail {{
            border-left-color: #ef4444;
        }}

        .metric-card h3 {{
            font-size: 1.25rem;
            margin-bottom: 1rem;
            color: #1e293b;
        }}

        .metric-value {{
            font-size: 2rem;
            font-weight: bold;
            color: #334155;
            margin-bottom: 0.5rem;
        }}

        .metric-detail {{
            color: #64748b;
            font-size: 0.9rem;
            margin-top: 0.5rem;
        }}

        .progress-bar {{
            width: 100%;
            height: 8px;
            background: #e2e8f0;
            border-radius: 4px;
            overflow: hidden;
            margin: 1rem 0;
        }}

        .progress-fill {{
            height: 100%;
            background: linear-gradient(90deg, #3b82f6, #8b5cf6);
            transition: width 0.3s ease;
        }}

        .progress-fill.pass {{
            background: linear-gradient(90deg, #10b981, #059669);
        }}

        .progress-fill.fail {{
            background: linear-gradient(90deg, #ef4444, #dc2626);
        }}

        .workflow-list {{
            margin-top: 1rem;
        }}

        .workflow-item {{
            background: white;
            padding: 0.75rem;
            margin-bottom: 0.5rem;
            border-radius: 0.25rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }}

        .workflow-name {{
            font-weight: 500;
            color: #1e293b;
        }}

        .workflow-status {{
            padding: 0.25rem 0.75rem;
            border-radius: 1rem;
            font-size: 0.85rem;
            font-weight: 500;
        }}

        .workflow-status.pass {{
            background: #d1fae5;
            color: #065f46;
        }}

        .workflow-status.fail {{
            background: #fee2e2;
            color: #991b1b;
        }}

        .badge {{
            display: inline-block;
            padding: 0.25rem 0.75rem;
            border-radius: 1rem;
            font-size: 0.85rem;
            font-weight: 500;
            margin-left: 0.5rem;
        }}

        .badge.success {{
            background: #d1fae5;
            color: #065f46;
        }}

        .badge.warning {{
            background: #fef3c7;
            color: #92400e;
        }}

        .badge.error {{
            background: #fee2e2;
            color: #991b1b;
        }}

        .timestamp {{
            text-align: center;
            padding: 1rem;
            color: #64748b;
            font-size: 0.9rem;
            border-top: 1px solid #e2e8f0;
        }}

        .detail-section {{
            padding: 2rem;
            border-top: 1px solid #e2e8f0;
        }}

        .detail-section h2 {{
            font-size: 1.5rem;
            color: #1e293b;
            margin-bottom: 1rem;
        }}

        .stats-row {{
            display: flex;
            gap: 2rem;
            margin-top: 1rem;
        }}

        .stat {{
            flex: 1;
        }}

        .stat-label {{
            color: #64748b;
            font-size: 0.875rem;
            margin-bottom: 0.25rem;
        }}

        .stat-value {{
            font-size: 1.5rem;
            font-weight: bold;
            color: #1e293b;
        }}
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>📊 Documentation Metrics Dashboard</h1>
            <p>machine-config-operator</p>
        </div>

        <div class="dashboard">
            <div class="score-banner">
                <div class="score-circle">
                    <div class="score-number">{overall_score}</div>
                    <div class="score-label">/100</div>
                </div>
                <h2>{overall_label}</h2>
                <p>Overall Documentation Quality</p>
            </div>

            <div class="metrics-grid">
                <!-- Navigation Depth -->
                <div class="metric-card {nav_metrics['status']}">
                    <h3>🧭 Navigation Depth</h3>
                    <div class="metric-value">{nav_metrics['max_depth']} hops</div>
                    <div class="progress-bar">
                        <div class="progress-fill {nav_metrics['status']}" style="width: {nav_score}%"></div>
                    </div>
                    <div class="metric-detail">
                        <strong>Score:</strong> {nav_score}/100
                        {'<span class="badge success">✓ PASSED</span>' if nav_metrics['status'] == 'pass' else '<span class="badge error">✗ FAILED</span>'}
                    </div>
                    <div class="metric-detail">
                        Average: {nav_metrics['avg_depth']:.1f} hops |
                        Reachable: {nav_metrics['reachable_docs']}/{nav_metrics['total_docs']} docs
                    </div>
                    {f'<div class="metric-detail"><span class="badge warning">{nav_metrics["unreachable_count"]} unreachable</span></div>' if nav_metrics['unreachable_count'] > 0 else ''}
                    {f'<div class="metric-detail"><span class="badge error">{nav_metrics["over_limit_count"]} over limit</span></div>' if nav_metrics['over_limit_count'] > 0 else ''}
                </div>

                <!-- Context Budget -->
                <div class="metric-card {budget_metrics['status']}">
                    <h3>📝 Context Budget</h3>
                    <div class="metric-value">{budget_metrics['max_observed']} lines</div>
                    <div class="progress-bar">
                        <div class="progress-fill {budget_metrics['status']}" style="width: {budget_score}%"></div>
                    </div>
                    <div class="metric-detail">
                        <strong>Score:</strong> {budget_score}/100
                        {'<span class="badge success">✓ PASSED</span>' if budget_metrics['status'] == 'pass' else '<span class="badge warning">⚠ OVER</span>'}
                    </div>
                    <div class="metric-detail">
                        Average: {budget_metrics['avg_observed']} lines |
                        Passing: {budget_metrics['passing']}/{budget_metrics['passing'] + budget_metrics['failing']} workflows
                    </div>
                </div>
            </div>

            <!-- Workflow Details -->
            <div class="detail-section">
                <h2>Workflow Analysis</h2>
                <div class="workflow-list">
"""

    # Add workflow items
    for workflow in budget_metrics.get('workflows', []):
        status_class = 'pass' if workflow.get('status') == 'pass' else 'fail'
        status_text = '✓ OK' if workflow.get('status') == 'pass' else '✗ OVER'
        actual = workflow.get('actual', 0)
        limit = workflow.get('limit', 700)

        html += f"""
                    <div class="workflow-item">
                        <div>
                            <div class="workflow-name">{workflow['name']}</div>
                            <small style="color: #64748b;">{actual}/{limit} lines</small>
                        </div>
                        <div class="workflow-status {status_class}">{status_text}</div>
                    </div>
"""

    html += f"""
                </div>
            </div>

            <!-- Statistics -->
            <div class="detail-section">
                <h2>Quick Stats</h2>
                <div class="stats-row">
                    <div class="stat">
                        <div class="stat-label">Total Documents</div>
                        <div class="stat-value">{nav_metrics['total_docs']}</div>
                    </div>
                    <div class="stat">
                        <div class="stat-label">Reachable</div>
                        <div class="stat-value">{nav_metrics['reachable_docs']}</div>
                    </div>
                    <div class="stat">
                        <div class="stat-label">Max Depth</div>
                        <div class="stat-value">{nav_metrics['max_depth']} hops</div>
                    </div>
                    <div class="stat">
                        <div class="stat-label">Avg Context</div>
                        <div class="stat-value">{budget_metrics['avg_observed']} lines</div>
                    </div>
                </div>
            </div>

            <div class="timestamp">
                Generated on {datetime.now().strftime('%Y-%m-%d %H:%M:%S')} by <code>scripts/generate-metrics-dashboard.py</code>
            </div>
        </div>
    </div>
</body>
</html>
"""

    output_path.write_text(html)
    return output_path


def main():
    parser = argparse.ArgumentParser(description='Generate HTML metrics dashboard')
    parser.add_argument('--output', '-o', default='agentic/metrics-dashboard.html',
                       help='Output HTML file path')
    parser.add_argument('--open', action='store_true',
                       help='Open dashboard in browser after generation')

    args = parser.parse_args()

    base_dir = Path.cwd()
    # Scripts are in agentic/scripts/ relative to repo root
    scripts_dir = base_dir / 'agentic' / 'scripts'

    # Handle being run from different locations
    if not scripts_dir.exists():
        # Maybe we're already in agentic/scripts/
        scripts_dir = Path(__file__).parent

    print("🔍 Running navigation depth analysis...")
    nav_result = run_metric_script(scripts_dir / 'measure-navigation-depth.py', '--max-depth', '3')

    print("🔍 Running context budget analysis...")
    budget_result = run_metric_script(scripts_dir / 'measure-context-budget.py', '--max-budget', '700')

    if not nav_result['success'] or not budget_result['success']:
        print("❌ Error running metric scripts", file=sys.stderr)
        if not nav_result['success']:
            print(f"Navigation error: {nav_result['error']}", file=sys.stderr)
        if not budget_result['success']:
            print(f"Budget error: {budget_result['error']}", file=sys.stderr)
        sys.exit(1)

    print("📊 Parsing metrics...")
    nav_metrics = parse_navigation_metrics(nav_result['output'])
    budget_metrics = parse_context_budget(budget_result['output'])

    print(f"✨ Generating HTML dashboard...")
    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    dashboard_path = generate_html_dashboard(nav_metrics, budget_metrics, output_path)

    print(f"✅ Dashboard generated: {dashboard_path}")

    if args.open:
        print("🌐 Opening in browser...")
        webbrowser.open(f'file://{dashboard_path.absolute()}')

    print(f"\n💡 To view: open {dashboard_path}")


if __name__ == '__main__':
    main()
