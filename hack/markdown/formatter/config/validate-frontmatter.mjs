// Parse and validate the leading YAML frontmatter in a Markdown document.
import fs from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(
  `${process.env.NPM_CONFIG_PREFIX ?? '/usr/local'}/lib/node_modules/validate-frontmatter.mjs`,
);
const { unified } = require('unified');
const remarkParse = require('remark-parse').default;
const remarkFrontmatter = require('remark-frontmatter').default;
const { parseDocument } = require('yaml');

const source = fs.readFileSync(process.argv[2], 'utf8');
const tree = unified().use(remarkParse).use(remarkFrontmatter, ['yaml']).parse(source);
const frontmatter = tree.children[0];

// Reject malformed YAML so delimiter-like content remains part of the document.
if (!frontmatter || frontmatter.type !== 'yaml' || parseDocument(frontmatter.value).errors.length) {
  process.exit(1);
}
