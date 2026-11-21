// Add Mermaid language labels to unlabeled diagram code blocks.
const MERMAID_KEYWORDS = /^\s*(graph|flowchart|sequenceDiagram|gantt|classDiagram|stateDiagram(-v2)?|erDiagram|journey|pie|gitGraph|mindmap|timeline|architecture-beta)\b/i;

function walk(node, visitor) {
  // Visit an mdast node and all of its descendants.
  visitor(node);
  if (node.children && Array.isArray(node.children)) {
    node.children.forEach((child) => walk(child, visitor));
  }
}

export default function remarkFixMermaid() {
  // Return the Remark transformer that identifies Mermaid code blocks.
  return (tree) => {
    walk(tree, (node) => {
      if (node.type === 'code') {
        // Mermaid directives precede the declaration used for language inference.
        const diagram = node.value.replace(/^\s*%%\{[\s\S]*?\}%%\s*/, '');
        if (!node.lang && MERMAID_KEYWORDS.test(diagram)) {
          node.lang = 'mermaid';
        }
      }
    });
  };
}
