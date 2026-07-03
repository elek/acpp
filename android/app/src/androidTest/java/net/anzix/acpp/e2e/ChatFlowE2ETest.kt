package net.anzix.acpp.e2e

import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import net.anzix.acpp.MainActivity
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Full end-to-end happy path against a live, separately-started ACPP backend
 * (docker postgres + `acpp web` running `rai acp fake` as the agent — see
 * `android/e2e/`). The test drives the real UI: Setup -> Projects -> new
 * session -> send a prompt -> observe the agent's reply.
 *
 * It is configured entirely through instrumentation arguments so it works on an
 * emulator (default `10.0.2.2`) or a real phone (pass the host's LAN IP):
 *
 *   -e ACPP_BASE_URL    http://10.0.2.2:6061
 *   -e ACPP_PROJECT_DIR /abs/path/on/server/to/android/e2e/projects/demo
 *
 * The `fake` agent emits random words for a plain prompt, so those assertions
 * are structural — that a reply bubble appears at all — not on the content. The
 * `[scenario1 count=N]` directive, by contrast, drives a *deterministic*
 * scripted turn (N iterations, each a `create` then a `cat` tool call), which
 * lets the second conversation assert the exact tool-call count and order.
 */
@RunWith(AndroidJUnit4::class)
class ChatFlowE2ETest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val args = InstrumentationRegistry.getArguments()
    private val baseUrl = args.getString("ACPP_BASE_URL") ?: "http://10.0.2.2:6061"
    private val projectDir =
        args.getString("ACPP_PROJECT_DIR")
            ?: "/home/elek/p/acpp/android/e2e/projects/demo"

    // Optional pause after each UI step so a human can watch the run unfold.
    // Set via `-e ACPP_STEP_DELAY_MS 1000` (the e2e script forwards STEP_DELAY_MS).
    // 0 (the default) makes the test run at full speed.
    private val stepDelayMs = args.getString("ACPP_STEP_DELAY_MS")?.toLongOrNull() ?: 0L

    @Test
    fun sendsPromptAndReceivesReply() {
        // --- Setup screen: point the app at the test server and connect. ---
        compose.awaitTag("setup_url", timeoutMillis = 30_000)
        step()
        compose.onNodeWithTag("setup_url").performTextInput(baseUrl)
        step()
        compose.onNodeWithTag("setup_connect").performClick()

        // --- Projects screen: the connect succeeded once the FAB shows. ---
        compose.awaitTag("projects_new_fab", timeoutMillis = 30_000)
        step()
        compose.onNodeWithTag("projects_new_fab").performClick()

        // --- New-session dialog: start a session in the seeded project dir. ---
        compose.awaitTag("newsession_dir", timeoutMillis = 5_000)
        step()
        compose.onNodeWithTag("newsession_dir").performTextInput(projectDir)
        step()
        compose.onNodeWithTag("newsession_create").performClick()

        // --- Conversation screen: type a prompt and send it. ---
        compose.awaitTag("composer_input", timeoutMillis = 20_000)
        step()
        compose.onNodeWithTag("composer_input").performTextInput("hello")
        step()
        // performClick blocks until the screen is idle again, i.e. until the
        // (fast) fake-agent turn has finished and the "Running…" animation stops.
        compose.onNodeWithTag("composer_send").performClick()

        // The user's prompt echoes into the transcript, and the fake agent
        // streams back a non-empty (random) reply bubble.
        compose.awaitTag("user_message", timeoutMillis = 10_000)
        compose.awaitTag("assistant_message", timeoutMillis = 30_000)
        step()

        assertTrue(
            "expected at least one assistant reply bubble",
            compose.onAllNodesWithTag("assistant_message")
                .fetchSemanticsNodes().isNotEmpty(),
        )

        // --- Second conversation: clear context to start a fresh session, then
        // drive the deterministic `scenario1` directive and assert on its
        // tool-call structure. ---
        startNewConversationViaClear()
        runScenario1()
    }

    /**
     * Opens the overflow menu and taps "Clear", which sends `/clear`. The
     * backend replaces the session and emits `session_replaced`, navigating the
     * UI to a brand-new, empty conversation. We confirm the swap by waiting for
     * the previous transcript to disappear (zero `user_message` bubbles) while a
     * usable composer is present again.
     */
    private fun startNewConversationViaClear() {
        compose.onNodeWithTag("conversation_menu").performClick()
        compose.awaitTag("menu_clear", timeoutMillis = 5_000)
        step()
        compose.onNodeWithTag("menu_clear").performClick()

        // Wait until the fresh (empty) conversation has loaded: the composer is
        // back and the old prompt/reply are gone.
        compose.waitUntil(20_000) {
            compose.onAllNodesWithTag("composer_input").fetchSemanticsNodes().isNotEmpty() &&
                compose.onAllNodesWithTag("user_message").fetchSemanticsNodes().isEmpty()
        }
        step()
    }

    /**
     * Sends `[scenario1 count=5]` and asserts the deterministic result: the fake
     * agent performs 5 iterations, each a `create` then a `cat` tool call, so
     * the transcript must contain exactly 10 tool-call rows in
     * `create, cat, …` order, all below the single user prompt.
     */
    private fun runScenario1() {
        compose.onNodeWithTag("composer_input").performTextInput("[scenario1 count=5]")
        step()
        compose.onNodeWithTag("composer_send").performClick()

        // Wait for all ten tool-call rows (5 × {create, cat}) to render.
        compose.awaitCount("tool_call", count = 10, timeoutMillis = 30_000)
        step()

        val rows = compose.onAllNodesWithTag("tool_call")
            .fetchSemanticsNodes()
            .sortedBy { it.boundsInRoot.top }
        assertEquals("expected exactly 10 tool-call rows", 10, rows.size)

        val titles = rows.map { node ->
            val text = node.collectText()
            when {
                text.contains("create") -> "create"
                text.contains("cat") -> "cat"
                else -> text
            }
        }
        assertEquals(
            "tool calls must appear as create/cat per iteration, in order",
            List(5) { listOf("create", "cat") }.flatten(),
            titles,
        )

        // Exactly one prompt for this conversation, and it sits above the first
        // tool call — i.e. prompt and tool calls are in the right order.
        val prompts = compose.onAllNodesWithTag("user_message").fetchSemanticsNodes()
        assertEquals("expected exactly one user prompt in the new conversation", 1, prompts.size)
        assertTrue(
            "user prompt must render above the first tool call",
            prompts.first().boundsInRoot.top < rows.first().boundsInRoot.top,
        )
    }

    /**
     * Polls for a tagged node to appear. Uses [androidx.compose.ui.test.ComposeTestRule.waitUntil]
     * (not `assertExists`/`waitForIdle`) so it never blocks on the conversation
     * screen's continuous progress/typing animations.
     */
    private fun AndroidComposeTestRule<*, *>.awaitTag(tag: String, timeoutMillis: Long) {
        waitUntil(timeoutMillis) {
            onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Polls until at least [count] nodes carry [tag]. */
    private fun AndroidComposeTestRule<*, *>.awaitCount(tag: String, count: Int, timeoutMillis: Long) {
        waitUntil(timeoutMillis) {
            onAllNodesWithTag(tag).fetchSemanticsNodes().size >= count
        }
    }

    /** Concatenates this node's text with all of its descendants' text. */
    private fun SemanticsNode.collectText(): String {
        val own = config.getOrNull(SemanticsProperties.Text)
            ?.joinToString(" ") { it.text }
            .orEmpty()
        val kids = children.joinToString(" ") { it.collectText() }
        return "$own $kids".trim()
    }

    /** Pauses [stepDelayMs] so a human can follow the run; a no-op when unset. */
    private fun step() {
        if (stepDelayMs > 0) Thread.sleep(stepDelayMs)
    }
}
