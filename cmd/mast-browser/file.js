"use strict";

var refsPane = document.getElementById("refs-pane");
var refsTitle = document.getElementById("refs-title");
var refsBody = document.getElementById("refs-body");
var refsClose = document.getElementById("refs-close");
var activeGroupId = null;

refsClose.addEventListener("click", function() {
	refsPane.classList.remove("open");
	clearHighlights();
	activeGroupId = null;
});

function clearHighlights() {
	var prev = document.querySelectorAll(".ident.highlight");
	for (var i = 0; i < prev.length; i++) {
		prev[i].classList.remove("highlight");
	}
}

document.getElementById("source-pane").addEventListener("click", function(e) {
	var el = e.target;
	while (el && !el.classList.contains("ident")) {
		if (el === e.currentTarget) { el = null; break; }
		el = el.parentElement;
	}
	if (!el) return;

	var gid = el.getAttribute("data-group");
	clearHighlights();

	if (!gid) {
		refsPane.classList.remove("open");
		activeGroupId = null;
		return;
	}

	if (gid === activeGroupId) {
		refsPane.classList.remove("open");
		activeGroupId = null;
		return;
	}
	activeGroupId = gid;

	var all = document.querySelectorAll('.ident[data-group="' + gid + '"]');
	for (var i = 0; i < all.length; i++) {
		all[i].classList.add("highlight");
	}

	fetch("/group?id=" + encodeURIComponent(gid))
		.then(function(r) { return r.json(); })
		.then(function(data) { renderRefs(data, gid); });
});

function renderRefs(data, gid) {
	if (gid !== activeGroupId) return;

	var defs = 0, uses = 0;
	for (var fi = 0; fi < data.files.length; fi++) {
		var snippets = data.files[fi].snippets;
		for (var si = 0; si < snippets.length; si++) {
			var hl = snippets[si].highlights;
			for (var hi = 0; hi < hl.length; hi++) {
				if (hl[hi].kind === "def") defs++;
				else uses++;
			}
		}
	}
	refsTitle.textContent = data.kind + " " + data.name + " (" + data.pkg + ") \u2014 " + defs + " def(s), " + uses + " use(s)";

	while (refsBody.firstChild) refsBody.removeChild(refsBody.firstChild);

	for (var fi = 0; fi < data.files.length; fi++) {
		var fileData = data.files[fi];
		var groupDiv = document.createElement("div");
		groupDiv.className = "ref-group";

		var header = document.createElement("div");
		header.className = "ref-file-header";
		var link = document.createElement("a");
		link.href = "/file?path=" + encodeURIComponent(fileData.file);
		link.target = "_self";
		link.textContent = fileData.file;
		header.appendChild(link);
		groupDiv.appendChild(header);

		for (var si = 0; si < fileData.snippets.length; si++) {
			var snippet = fileData.snippets[si];
			var snippetDiv = document.createElement("div");
			snippetDiv.className = "ref-snippet";

			// Index highlights by line for quick lookup.
			// Multiple highlights can exist on the same line.
			var hlByLine = {};
			for (var hi = 0; hi < snippet.highlights.length; hi++) {
				var h = snippet.highlights[hi];
				if (!(h.line in hlByLine)) hlByLine[h.line] = [];
				hlByLine[h.line].push(h);
			}

			for (var ci = 0; ci < snippet.context.length; ci++) {
				var lineNum = snippet.contextStart + ci;
				var lineText = snippet.context[ci];

				var lineDiv = document.createElement("div");
				lineDiv.className = "ref-line";

				var numSpan = document.createElement("span");
				numSpan.className = "ref-line-num";
				numSpan.textContent = String(lineNum);
				lineDiv.appendChild(numSpan);

				var contentSpan = document.createElement("span");
				contentSpan.className = "ref-line-content";

				if (lineNum in hlByLine) {
					// Render line with highlighted ident spans.
					var hls = hlByLine[lineNum].slice();
					hls.sort(function(a, b) { return a.col - b.col; });
					var cursor = 0;
					for (var hli = 0; hli < hls.length; hli++) {
						var hl = hls[hli];
						var colStart = hl.col - 1; // 0-based
						var colEnd = colStart + hl.len;
						if (colStart > cursor) {
							contentSpan.appendChild(document.createTextNode(lineText.substring(cursor, colStart)));
						}
						var identSpan = document.createElement("span");
						identSpan.className = "ref-ident " + hl.kind;
						identSpan.textContent = lineText.substring(colStart, colEnd);
						contentSpan.appendChild(identSpan);
						cursor = colEnd;
					}
					if (cursor < lineText.length) {
						contentSpan.appendChild(document.createTextNode(lineText.substring(cursor)));
					}
				} else {
					contentSpan.textContent = lineText;
				}

				lineDiv.appendChild(contentSpan);
				snippetDiv.appendChild(lineDiv);
			}

			groupDiv.appendChild(snippetDiv);
		}

		refsBody.appendChild(groupDiv);
	}

	refsPane.classList.add("open");
}
