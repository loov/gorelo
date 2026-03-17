"use strict";

// Theme toggle: cycles system -> light -> dark -> system.
// Persists choice in localStorage and syncs across frames.

var STORAGE_KEY = "mast-browser-theme";
var THEMES = ["system", "light", "dark"];
var LABELS = { system: "auto", light: "light", dark: "dark" };

function currentTheme() {
	return localStorage.getItem(STORAGE_KEY) || "system";
}

function applyTheme(theme) {
	var root = document.documentElement;
	root.removeAttribute("data-theme");
	if (theme === "light" || theme === "dark") {
		root.setAttribute("data-theme", theme);
	}
	var btn = document.getElementById("theme-toggle");
	if (btn) {
		btn.textContent = "[" + LABELS[theme] + "]";
		btn.title = "Theme: " + theme + " (click to cycle)";
	}
}

function cycleTheme() {
	var current = currentTheme();
	var next = THEMES[(THEMES.indexOf(current) + 1) % THEMES.length];
	localStorage.setItem(STORAGE_KEY, next);
	applyTheme(next);

	// Sync to iframe if present.
	var iframe = document.querySelector("iframe[name=src]");
	if (iframe && iframe.contentDocument) {
		var iframeRoot = iframe.contentDocument.documentElement;
		iframeRoot.removeAttribute("data-theme");
		if (next === "light" || next === "dark") {
			iframeRoot.setAttribute("data-theme", next);
		}
	}
}

// Apply on load.
applyTheme(currentTheme());

// Listen for storage changes from other frames.
window.addEventListener("storage", function (e) {
	if (e.key === STORAGE_KEY) {
		applyTheme(e.newValue || "system");
	}
});

// Bind toggle button.
var toggleBtn = document.getElementById("theme-toggle");
if (toggleBtn) {
	toggleBtn.addEventListener("click", cycleTheme);
}
