package browsers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxArticleRunes caps Article.Text; Article.Truncated reports the cut.
const MaxArticleRunes = 40000

// Article is the readable body of a page as markdown plus the metadata an agent
// needs to cite it.
type Article struct {
	// Title is document.title, falling back to the first h1.
	Title string `json:"title"`
	// Byline is the author metadata, empty when the page declares none.
	Byline string `json:"byline,omitempty"`
	// Excerpt is the page description metadata, empty when absent.
	Excerpt string `json:"excerpt,omitempty"`
	// Text is the extracted body as markdown, capped at MaxArticleRunes runes.
	Text string `json:"text"`
	// Words is the word count of the emitted Text.
	Words int `json:"words"`
	// Truncated reports whether the rune cap cut the extracted body.
	Truncated bool `json:"truncated"`
	// URL is the page's location at extraction time.
	URL string `json:"url,omitempty"`
}

// articlePayload is the in-page extractor's raw result: the derived word count
// and the rune cap belong to Go, not the script.
type articlePayload struct {
	Title   string `json:"title"`
	Byline  string `json:"byline"`
	Excerpt string `json:"excerpt"`
	Text    string `json:"text"`
	URL     string `json:"url"`
}

// Article extracts a page's main readable content as markdown: candidate
// scoring picks the densest article/main/section/div root, page chrome is
// stripped, and the body renders headings, paragraphs, lists, code, quotes,
// links, images and tables. Text is capped at MaxArticleRunes runes.
//
// The method takes the page lock itself (mirroring Page.A11yNodes): callers
// must NOT pre-lock, and the nil-Rod guard runs under the lock. Extraction is a
// single in-page script, so no DOM crosses the wire.
func (p *Page) Article(ctx context.Context) (Article, error) {
	if err := p.lockRead(ctx, "article"); err != nil {
		return Article{}, err
	}
	defer p.Unlock()
	if p.Rod == nil {
		return Article{}, fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(articleJS)
	if err != nil {
		return Article{}, fmt.Errorf("extract article: %w", err)
	}
	if res.Value.Nil() {
		return Article{}, fmt.Errorf("extract article: empty result")
	}

	payload, err := json.Marshal(res.Value)
	if err != nil {
		return Article{}, fmt.Errorf("extract article: %w", err)
	}
	var raw articlePayload
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Article{}, fmt.Errorf("extract article: %w", err)
	}
	text, truncated := truncateRunes(raw.Text, MaxArticleRunes)
	return Article{
		Title:     raw.Title,
		Byline:    raw.Byline,
		Excerpt:   raw.Excerpt,
		Text:      text,
		Words:     len(strings.Fields(text)),
		Truncated: truncated,
		URL:       raw.URL,
	}, nil
}

// truncateRunes caps s at max runes on a rune boundary and reports the cut.
func truncateRunes(s string, max int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	return string(runes[:max]), true
}

// articleJS extracts the readable body as markdown in one expression: score the
// candidate roots, clone the winner, drop chrome from the clone, then render it
// block by block. Backticks are built with fromCharCode so the source stays a
// Go raw string.
const articleJS = `() => {
  const SKIP = 'script, style, noscript, iframe, svg, form, button, input, select, textarea, nav, aside, footer, header, object, canvas, sup, [role=navigation], [role=banner], [role=contentinfo], [role=complementary], [role=search], [aria-hidden="true"], [role=dialog], [aria-modal="true"], [class*=banner i], [class*=cookie i], [class*=consent i], [class*=donat i], [class*=subscribe i], [class*=newsletter i], [class*=paywall i], [id*=catlinks i], [class*=catlinks i]';
  const NOISE_SECTION = /^(see also|references|external links|further reading|notes|bibliography|citations|sources|related articles)$/i;
  const CANDIDATES = 'article, main, [role=main], section, div';
  const BLOCK = new Set([
    'ADDRESS', 'ARTICLE', 'ASIDE', 'BLOCKQUOTE', 'DD', 'DIV', 'DL', 'DT', 'FIELDSET',
    'FIGCAPTION', 'FIGURE', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'HR', 'IMG', 'LI',
    'MAIN', 'OL', 'P', 'PRE', 'SECTION', 'TABLE', 'TR', 'UL'
  ]);
  const FENCE = String.fromCharCode(96, 96, 96);

  function collapse(value) {
    return (value || '').replace(/\s+/g, ' ').trim();
  }

  function absolute(url) {
    if (!url) {
      return '';
    }
    try {
      return new URL(url, location.href).href;
    } catch (err) {
      return '';
    }
  }

  function blockChars(el) {
    let total = 0;
    for (const block of el.querySelectorAll('p, pre, td, blockquote')) {
      total += collapse(block.textContent).length;
    }
    return total;
  }

  function score(el) {
    const total = collapse(el.textContent).length;
    if (!total) {
      return 0;
    }
    let linkChars = 0;
    for (const link of el.querySelectorAll('a')) {
      linkChars += collapse(link.textContent).length;
    }
    if (linkChars / total > 0.4) {
      return 0;
    }
    return blockChars(el) + el.querySelectorAll('p').length * 10;
  }

  function pickRoot() {
    let explicit = null;
    let explicitChars = 0;
    for (const el of document.querySelectorAll('main, article, [role=main]')) {
      const chars = collapse(el.textContent).length;
      if (chars > explicitChars) {
        explicit = el;
        explicitChars = chars;
      }
    }
    if (explicit && explicitChars > 400) {
      return explicit;
    }
    let best = null;
    let bestScore = 0;
    let bestChars = Infinity;
    for (const candidate of document.querySelectorAll(CANDIDATES)) {
      const candidateScore = score(candidate);
      if (candidateScore === 0) {
        continue;
      }
      const candidateChars = collapse(candidate.textContent).length;
      if (candidateScore > bestScore || (candidateScore === bestScore && candidateChars < bestChars)) {
        best = candidate;
        bestScore = candidateScore;
        bestChars = candidateChars;
      }
    }
    return best || document.body || document.documentElement;
  }

  function image(el) {
    const src = absolute(el.getAttribute('src'));
    if (!src || src.toLowerCase().startsWith('data:')) {
      return '';
    }
    if (el.getAttribute('width') === '1' && el.getAttribute('height') === '1') {
      return '';
    }
    return '![' + collapse(el.getAttribute('alt')) + '](' + src + ')';
  }

  function inline(el) {
    let out = '';
    for (const node of el.childNodes) {
      if (node.nodeType === 3) {
        out += node.nodeValue.replace(/\s+/g, ' ');
        continue;
      }
      if (node.nodeType !== 1) {
        continue;
      }
      const tag = node.tagName.toLowerCase();
      if (tag === 'a') {
        const label = collapse(inline(node));
        if (!label) {
          continue;
        }
        const href = absolute(node.getAttribute('href'));
        if (!href || href.toLowerCase().startsWith('javascript:')) {
          out += label;
        } else {
          out += '[' + label + '](' + href + ')';
        }
        continue;
      }
      if (tag === 'img') {
        out += image(node);
        continue;
      }
      if (tag === 'br') {
        out += '\n';
        continue;
      }
      out += inline(node);
    }
    return out;
  }

  function list(el, depth) {
    const ordered = el.tagName.toLowerCase() === 'ol';
    const pad = '  '.repeat(depth);
    let out = '';
    let index = 1;
    for (const item of el.children) {
      if (item.tagName.toLowerCase() !== 'li') {
        continue;
      }
      let text = '';
      const nested = [];
      for (const node of item.childNodes) {
        if (node.nodeType === 3) {
          text += node.nodeValue.replace(/\s+/g, ' ');
          continue;
        }
        if (node.nodeType !== 1) {
          continue;
        }
        const tag = node.tagName.toLowerCase();
        if (tag === 'ul' || tag === 'ol') {
          nested.push(node);
        } else if (BLOCK.has(node.tagName)) {
          text += ' ' + inline(node);
        } else {
          text += inline(node);
        }
      }
      const marker = ordered ? index + '. ' : '- ';
      index += 1;
      const line = collapse(text);
      if (line) {
        out += pad + marker + line + '\n';
      }
      for (const child of nested) {
        out += list(child, depth + 1);
      }
    }
    return out + '\n';
  }

  function table(el) {
    const rows = [];
    for (const row of el.querySelectorAll('tr')) {
      const cells = [];
      for (const cell of row.children) {
        const tag = cell.tagName.toLowerCase();
        if (tag !== 'td' && tag !== 'th') {
          continue;
        }
        cells.push(collapse(inline(cell)).replace(/\|/g, '\\|'));
      }
      if (cells.length) {
        rows.push(cells);
      }
    }
    if (!rows.length) {
      return '';
    }
    const width = Math.max(...rows.map((row) => row.length));
    let out = '';
    rows.forEach((row, index) => {
      while (row.length < width) {
        row.push('');
      }
      out += '| ' + row.join(' | ') + ' |\n';
      if (index === 0) {
        out += '| ' + row.map(() => '---').join(' | ') + ' |\n';
      }
    });
    return out + '\n';
  }

  function block(el, depth) {
    const tag = el.tagName.toLowerCase();
    if (/^h[1-6]$/.test(tag)) {
      const text = collapse(inline(el));
      return text ? '#'.repeat(Number(tag[1])) + ' ' + text + '\n\n' : '';
    }
    if (tag === 'p') {
      const text = collapse(inline(el));
      return text ? text + '\n\n' : '';
    }
    if (tag === 'ul' || tag === 'ol') {
      return list(el, depth);
    }
    if (tag === 'pre') {
      const code = el.textContent.replace(/^\n+|\s+$/g, '');
      return code.trim() ? FENCE + '\n' + code + '\n' + FENCE + '\n\n' : '';
    }
    if (tag === 'blockquote') {
      const inner = children(el, depth).trim();
      if (!inner) {
        return '';
      }
      return inner.split('\n').map((line) => (line ? '> ' + line : '>')).join('\n') + '\n\n';
    }
    if (tag === 'table') {
      return table(el);
    }
    if (tag === 'hr') {
      return '---\n\n';
    }
    if (tag === 'img') {
      const markdown = image(el);
      return markdown ? markdown + '\n\n' : '';
    }
    return children(el, depth);
  }

  function children(el, depth) {
    let out = '';
    let pending = '';
    for (const node of el.childNodes) {
      if (node.nodeType === 3) {
        pending += node.nodeValue.replace(/\s+/g, ' ');
        continue;
      }
      if (node.nodeType !== 1) {
        continue;
      }
      if (BLOCK.has(node.tagName)) {
        if (pending.trim()) {
          out += pending.trim() + '\n\n';
        }
        pending = '';
        out += block(node, depth);
      } else {
        pending += inline(node);
      }
    }
    if (pending.trim()) {
      out += pending.trim() + '\n\n';
    }
    return out;
  }

  function meta(selector) {
    const el = document.querySelector(selector);
    return el ? collapse(el.getAttribute('content')) : '';
  }

  const clone = pickRoot().cloneNode(true);
  for (const noise of clone.querySelectorAll(SKIP)) {
    noise.remove();
  }

  for (const heading of Array.from(clone.querySelectorAll('h1, h2'))) {
    if (!NOISE_SECTION.test(collapse(heading.textContent))) {
      continue;
    }
    let stop = heading;
    if (stop.parentElement && stop.parentElement !== clone && stop.parentElement.children.length === 1) {
      stop = stop.parentElement;
    }
    let node = stop;
    while (node) {
      const next = node.nextElementSibling;
      node.remove();
      node = next;
    }
  }

  const text = children(clone, 0)
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();

  const heading = document.querySelector('h1');
  const title = collapse(document.title) || (heading ? collapse(heading.textContent) : '');

  let byline = meta('meta[name="author"]');
  if (!byline) {
    const author = document.querySelector('[rel=author], [itemprop=author]');
    if (author) {
      byline = collapse(author.textContent);
    }
  }

  const excerpt = meta('meta[name="description"]') || meta('meta[property="og:description"]');

  return {
    title: title,
    byline: byline,
    excerpt: excerpt,
    text: text,
    url: location.href
  };
}`
