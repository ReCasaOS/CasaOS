// Package fleet lets one box be the master of the owner's other boxes: each
// member keeps an outbound tunnel to the master, and the master relays an
// allow-listed set of each box's own API routes so one dashboard sees and
// acts on all of them.
package fleet
