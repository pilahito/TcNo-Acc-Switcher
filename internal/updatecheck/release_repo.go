package updatecheck

// ReleaseRepository is the GitHub repository this build updates itself from.
//
// This fork (pilahito/TcNo-Acc-Switcher) publishes its own releases, signed
// with its own key (updater-key.pub), so the in-app updater must look there
// and not at TCNOco/TcNo-Acc-Switcher: an official release is signed with a
// different key and would be refused anyway, and if it were not, installing it
// would silently remove the fork's changes.
const ReleaseRepository = "pilahito/TcNo-Acc-Switcher"

// ReleasesPageURL is where "download manually" links point.
const ReleasesPageURL = "https://github.com/" + ReleaseRepository + "/releases/latest"

// UsesTcnoVersionAPI reports whether the tcno.co version API may be used as an
// update-check fallback. That API describes official builds only; a fork
// asking it would be told about versions it cannot (and should not) install.
const UsesTcnoVersionAPI = false
