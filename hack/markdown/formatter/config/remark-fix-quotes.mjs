// Normalize word-processor punctuation in prose while preserving Markdown data.
function walk(node, visitor) {
  // Visit an mdast node and all of its descendants.
  visitor(node);
  if (node.children && Array.isArray(node.children)) {
    node.children.forEach((child) => walk(child, visitor));
  }
}

export default function remarkStripWordProcessorArtifacts() {
  // Return the Remark transformer that cleans prose text nodes.
  return (tree) => {
    walk(tree, (node) => {
      // Frontmatter and code are data, so only normalize prose text nodes.
      if (node.type === 'text' && typeof node.value === 'string') {
        node.value = node.value
          .replace(/[“”]/g, '"')
          .replace(/[‘’]/g, "'")
          .replace(/–/g, '-')
          .replace(/—/g, '--')
          .replace(/…/g, '...')
          .replace(/\u00a0/g, ' ');
      }
    });
  };
}
