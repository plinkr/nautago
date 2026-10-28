# Test fixtures

`landing.html`, `login_page.html` and `logged_in.html` are verbatim captures of
the pages the Nauta captive portal serves, taken from the NautaPy test suite.
They are the reference for the markup this package has to parse, so they are
kept unmodified rather than replaced by something easier to read.

`failed_login.html`, `logout_success.html`, `logout_failure.html` and
`session_info.html` were written for this package. They mirror the same
structure for responses NautaPy has no captures of.
