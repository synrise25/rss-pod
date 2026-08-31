package webui

import "embed"

// Files contains the production player UI. Keeping the assets embedded lets the
// Go service expose the player and its read-only API from one origin.
//
//go:embed index.html app.css app.js demo.mp3 icons/*
var Files embed.FS
