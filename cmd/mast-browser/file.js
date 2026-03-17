"use strict";

// --- DOM references ---

var sourcePane = document.getElementById("source-pane");
var refsPane = document.getElementById("refs-pane");
var refsTitle = document.getElementById("refs-title");
var refsBody = document.getElementById("refs-body");
var activeGroupId = null;

// --- Refs panel ---

function openRefsPanel(gid) {
	activeGroupId = gid;
	highlightGroup(gid);
	fetch("/group?id=" + encodeURIComponent(gid))
		.then(function (r) {
			return r.json();
		})
		.then(function (data) {
			if (activeGroupId !== gid) return;
			renderRefs(data);
			refsPane.classList.add("open");
		});
}

function closeRefsPanel() {
	activeGroupId = null;
	clearHighlights();
	refsPane.classList.remove("open");
}

// --- Source highlighting ---

function highlightGroup(gid) {
	clearHighlights();
	var els = document.querySelectorAll('.ident[data-group="' + gid + '"]');
	for (var i = 0; i < els.length; i++) {
		els[i].classList.add("highlight");
	}
}

function clearHighlights() {
	var els = document.querySelectorAll(".ident.highlight");
	for (var i = 0; i < els.length; i++) {
		els[i].classList.remove("highlight");
	}
}

// --- Event handlers ---

document.getElementById("refs-close").addEventListener("click", closeRefsPanel);

sourcePane.addEventListener("click", function (e) {
	var el = e.target;
	while (el && !el.classList.contains("ident")) {
		if (el === sourcePane) return;
		el = el.parentElement;
	}
	if (!el) return;

	var gid = el.getAttribute("data-group");
	if (!gid || gid === activeGroupId) {
		closeRefsPanel();
	} else {
		openRefsPanel(gid);
	}
});

// --- Refs rendering ---
//
// The /group?id=N endpoint returns JSON with the following structure:
//
//   {
//     name: string,          // identifier name
//     kind: string,          // "type", "func", "method", "field", "var", "const", "package", "label"
//     pkg:  string,          // package path where defined
//     files: [{
//       file: string,        // relative file path
//       snippets: [{
//         contextStart: int, // 1-based line number of first context line
//         context: [string], // source lines surrounding the references
//         highlights: [{
//           line: int,       // 1-based line number
//           col:  int,       // 1-based byte column
//           len:  int,       // identifier length in bytes
//           kind: string     // "def" or "use"
//         }]
//       }]
//     }]
//   }

function renderRefs(data) {
	refsTitle.textContent = formatRefsTitle(data);
	while (refsBody.firstChild) refsBody.removeChild(refsBody.firstChild);
	for (var i = 0; i < data.files.length; i++) {
		refsBody.appendChild(renderFileGroup(data.files[i]));
	}
}

function formatRefsTitle(data) {
	var defs = 0,
		uses = 0;
	for (var fi = 0; fi < data.files.length; fi++) {
		var snippets = data.files[fi].snippets;
		for (var si = 0; si < snippets.length; si++) {
			var hls = snippets[si].highlights;
			for (var hi = 0; hi < hls.length; hi++) {
				if (hls[hi].kind === "def") defs++;
				else uses++;
			}
		}
	}
	return (
		data.kind +
		" " +
		data.name +
		" (" +
		data.pkg +
		") \u2014 " +
		defs +
		" def(s), " +
		uses +
		" use(s)"
	);
}

function renderFileGroup(file) {
	var div = document.createElement("div");
	div.className = "ref-group";

	var header = document.createElement("div");
	header.className = "ref-file-header";
	var link = document.createElement("a");
	link.href = "/file?path=" + encodeURIComponent(file.file);
	link.textContent = file.file;
	header.appendChild(link);
	div.appendChild(header);

	for (var i = 0; i < file.snippets.length; i++) {
		div.appendChild(renderSnippet(file.snippets[i]));
	}
	return div;
}

function renderSnippet(snippet) {
	var div = document.createElement("div");
	div.className = "ref-snippet";

	// Index highlights by line number.
	var hlByLine = {};
	for (var i = 0; i < snippet.highlights.length; i++) {
		var h = snippet.highlights[i];
		if (!hlByLine[h.line]) hlByLine[h.line] = [];
		hlByLine[h.line].push(h);
	}

	for (var i = 0; i < snippet.context.length; i++) {
		var lineNum = snippet.contextStart + i;
		var lineText = snippet.context[i];
		div.appendChild(renderLine(lineNum, lineText, hlByLine[lineNum]));
	}
	return div;
}

function renderLine(lineNum, text, highlights) {
	var div = document.createElement("div");
	div.className = "ref-line";

	var numSpan = document.createElement("span");
	numSpan.className = "ref-line-num";
	numSpan.textContent = String(lineNum);
	div.appendChild(numSpan);

	var content = document.createElement("span");
	content.className = "ref-line-content";
	if (highlights) {
		renderHighlightedText(content, text, highlights);
	} else {
		content.textContent = text;
	}
	div.appendChild(content);

	return div;
}

function renderHighlightedText(container, text, highlights) {
	highlights.sort(function (a, b) {
		return a.col - b.col;
	});
	var cursor = 0;
	for (var i = 0; i < highlights.length; i++) {
		var start = highlights[i].col - 1;
		var end = start + highlights[i].len;
		if (start > cursor) {
			container.appendChild(
				document.createTextNode(text.substring(cursor, start)),
			);
		}
		var span = document.createElement("span");
		span.className = "ref-ident " + highlights[i].kind;
		span.textContent = text.substring(start, end);
		container.appendChild(span);
		cursor = end;
	}
	if (cursor < text.length) {
		container.appendChild(document.createTextNode(text.substring(cursor)));
	}
}
