"""Tests of the pure parts of the accounts proof. Run: python -I dev/accounts-proof/test_routes.py"""
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("routes", os.path.join(HERE, "routes.py"))
routes = importlib.util.module_from_spec(spec)
spec.loader.exec_module(routes)

ROUTES = """# generated
GET /ping
GET /v1/users/current
GET /v1/users/:username
DELETE /v1/users/:id
POST /v1/sys/stop
ANY /v1/zt/*url
GET /v2/app_management/stats/{id}
GET /v2/app_management/stats/{id}/alerts
PUT /v2/app_management/stats/{id}/alerts
GET /v2/app_management/compose/{id}
GET /v2/app_management/appstore
DELETE /v2/app_management/appstore/{id}
GET /v2/app_management/apps/{id}
GET /v2/app_management/backup/destinations/{name}/runs/{app}/{stamp}
GET /v2/app_management/git/{app}
PUT /v2/app_management/visibility/{app}
GET /v1/port/
"""

TABLE_GO = '''var allowTable = []rule{
	{"GET", "/v1/users/status", public},                  // the login page
	{"GET", "/v1/users/current", base},
	{"GET", "/v2/app_management/stats/*", base},            // one series
	{"GET", "/v2/app_management/stats/*/alerts", base},
	{"GET", "/v1/recover/*/*", public},
	{"POST", "/v1/rights/*", module("storage")},
}'''

UI_CALLS = """# header
GET /v1/users/status public

GET /v2/app_management/stats/app:immich base
PUT /v1/users/current base
GET /v1/disks module:storage
"""


class RoutesTest(unittest.TestCase):
    def test_parse_routes_skips_comments_and_expands_any(self):
        got = routes.parse_routes(ROUTES)
        self.assertIn(("GET", "/ping"), got)
        self.assertIn(("GET", "/v1/zt/*url"), got)
        self.assertIn(("POST", "/v1/zt/*url"), got)
        self.assertNotIn(("ANY", "/v1/zt/*url"), got)
        self.assertTrue(all(not m.startswith("#") for m, _ in got))

    def test_concretize_fills_every_parameter_shape(self):
        self.assertEqual(routes.concretize("/v1/users/:id"), "/v1/users/x")
        self.assertEqual(routes.concretize("/v2/a/{id}/b/{name}"), "/v2/a/x/b/x")
        self.assertEqual(routes.concretize("/v1/zt/*url"), "/v1/zt/x")
        self.assertEqual(routes.concretize("/v1/port/"), "/v1/port/")
        self.assertEqual(routes.concretize("/v1/users/current"), "/v1/users/current")
        self.assertEqual(routes.concretize("/v2/a/{id}", "proofhidden"), "/v2/a/proofhidden")

    def test_table_rows_come_from_the_go_file_without_module_rows(self):
        rows = routes.parse_table_go(TABLE_GO)
        self.assertEqual(len(rows), 5)
        self.assertIn(("GET", "/v1/recover/*/*", "public"), rows)
        self.assertFalse([r for r in rows if r[1] == "/v1/rights/*"])

    def test_allowed_matches_one_segment_per_star_and_opens_head_on_get(self):
        rows = routes.parse_table_go(TABLE_GO)
        self.assertTrue(routes.allowed("GET", "/v2/app_management/stats/x", rows))
        self.assertTrue(routes.allowed("HEAD", "/v2/app_management/stats/x", rows))
        self.assertTrue(routes.allowed("GET", "/v2/app_management/stats/x/alerts", rows))
        self.assertFalse(routes.allowed("PUT", "/v2/app_management/stats/x/alerts", rows))
        self.assertFalse(routes.allowed("GET", "/v2/app_management/stats/x/y/z", rows))
        self.assertFalse(routes.allowed("GET", "/v1/users/x", rows))
        self.assertTrue(routes.allowed("GET", "/v1/recover/a/b", rows))
        self.assertFalse(routes.allowed("GET", "/v1/recover/a", rows))

    def test_plan_marks_denied_routes_for_a_user(self):
        rows = routes.parse_table_go(TABLE_GO)
        plan = routes.plan(routes.parse_routes(ROUTES), rows)
        denied = {(m, p) for m, p, e in plan if e == "denied"}
        opened = {(m, p) for m, p, e in plan if e == "open"}
        self.assertIn(("GET", "/v1/users/x"), denied)
        self.assertIn(("DELETE", "/v1/users/x"), denied)
        self.assertIn(("POST", "/v1/sys/stop"), denied)
        self.assertIn(("PUT", "/v2/app_management/stats/x/alerts"), denied)
        self.assertIn(("GET", "/ping"), opened, "the Gateway's own ping is open to any token")
        self.assertIn(("GET", "/v1/users/current"), opened)
        self.assertIn(("GET", "/v2/app_management/stats/x/alerts"), opened)
        self.assertEqual(len(plan), len(set(plan)))

    def test_table_rows_with_ui_calls_are_a_union(self):
        rows = routes.parse_table_go(TABLE_GO) + routes.parse_ui_calls(UI_CALLS)
        self.assertTrue(routes.allowed("PUT", "/v1/users/current", rows))

    def test_a_module_line_of_the_ui_calls_opens_nothing_to_a_user_without_the_right(self):
        rows = routes.parse_table_go(TABLE_GO) + routes.parse_ui_calls(UI_CALLS)
        plan = routes.plan([("GET", "/v1/disks"), ("PUT", "/v1/users/current")], rows)
        self.assertIn(("GET", "/v1/disks", "denied"), plan)
        self.assertIn(("PUT", "/v1/users/current", "open"), plan)

    def test_jobj_escapes_what_it_is_given(self):
        import json
        tricky = 'a"b\\c'
        doc = routes.jobj(["username", tricky, "password", "p w"])
        self.assertEqual(json.loads(doc), {"username": tricky, "password": "p w"})

    def test_ui_calls_take_the_fixture_names(self):
        calls = routes.ui_calls(UI_CALLS, "proofshown")
        self.assertEqual(calls, [
            ("GET", "/v1/users/status", "public"),
            ("GET", "/v2/app_management/stats/app:proofshown", "base"),
            ("PUT", "/v1/users/current", "base"),
            ("GET", "/v1/disks", "module:storage"),
        ])

    def test_app_routes_are_the_per_app_ones_of_app_management(self):
        got = routes.app_routes(routes.parse_routes(ROUTES), "proofhidden")
        paths = {(m, p) for m, p in got}
        self.assertIn(("GET", "/v2/app_management/stats/proofhidden"), paths)
        self.assertIn(("PUT", "/v2/app_management/stats/proofhidden/alerts"), paths)
        self.assertIn(("GET", "/v2/app_management/compose/proofhidden"), paths)
        self.assertIn(("GET", "/v2/app_management/git/proofhidden"), paths)
        # the store's ids, the owner's visibility routes and other kinds of names are not app names
        self.assertFalse([p for _, p in paths if "appstore" in p or "/apps/" in p or "visibility" in p or "destinations" in p])

    def test_ui_copy_equal_ignores_only_the_copy_line(self):
        gateway = "# a\nGET /x public\n"
        self.assertTrue(routes.ui_copy_equal("# COPY of CasaOS-Gateway route/testdata/ui-calls.txt (verbatim)\n" + gateway, gateway))
        self.assertFalse(routes.ui_copy_equal("# COPY of x\n# a\nGET /y public\n", gateway))
        self.assertFalse(routes.ui_copy_equal(gateway, gateway), "a copy with no header line is not the committed copy")
        self.assertTrue(routes.ui_copy_equal("# COPY of x\r\n# a\r\nGET /x public\r\n", gateway), "line endings are not content")

    def test_same_answer_replaces_each_name_by_a_mark(self):
        a = '{"message":"app proofhidden not found"}'
        b = '{"message":"app proofnone not found"}'
        self.assertTrue(routes.same_answer((404, a), "proofhidden", (404, b), "proofnone"))
        self.assertFalse(routes.same_answer((404, a), "proofhidden", (403, b), "proofnone"))
        self.assertFalse(routes.same_answer((404, a), "proofhidden", (404, '{"message":"forbidden"}'), "proofnone"))

    def test_jget_walks_keys_and_list_indexes(self):
        doc = '{"data":{"token":{"access_token":"t"},"list":[{"id":7}]}}'
        self.assertEqual(routes.jget(doc, "data.token.access_token"), "t")
        self.assertEqual(routes.jget(doc, "data.list.0.id"), "7")
        self.assertEqual(routes.jget(doc, "data.nothing"), "")
        self.assertEqual(routes.jget("not json", "a"), "")

    def test_user_id_by_name_reads_the_accounts_list(self):
        doc = '{"data":[{"id":1,"username":"a"},{"id":4,"username":"b"}]}'
        self.assertEqual(routes.jfind(doc, "data", "username", "b", "id"), "4")
        self.assertEqual(routes.jfind(doc, "data", "username", "zz", "id"), "")


if __name__ == "__main__":
    unittest.main()
