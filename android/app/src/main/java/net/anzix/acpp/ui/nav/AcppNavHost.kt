package net.anzix.acpp.ui.nav

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.toRoute
import net.anzix.acpp.ui.conversation.ConversationScreen
import net.anzix.acpp.ui.conversations.ConversationsScreen
import net.anzix.acpp.ui.projects.ProjectsScreen
import net.anzix.acpp.ui.setup.SetupScreen

// Real screens are wired in per-phase; remaining destinations stay as placeholders
// until their phase lands.
@Composable
fun AcppNavHost(startConfigured: Boolean = false) {
    val navController = rememberNavController()
    NavHost(
        navController = navController,
        startDestination = if (startConfigured) Projects else Setup,
    ) {
        composable<Setup> {
            SetupScreen(
                onConfigured = {
                    navController.navigate(Projects) {
                        popUpTo<Setup> { inclusive = true }
                    }
                },
            )
        }
        composable<Projects> {
            ProjectsScreen(
                onOpenProject = { name -> navController.navigate(Conversations(name)) },
                onOpenConversation = { id -> navController.navigate(Conversation(id)) },
            )
        }
        composable<Conversations> {
            ConversationsScreen(
                onBack = { navController.popBackStack() },
                onOpenConversation = { id -> navController.navigate(Conversation(id)) },
            )
        }
        composable<Conversation> {
            ConversationScreen(
                onBack = { navController.popBackStack() },
                onSessionReplaced = { newId ->
                    navController.navigate(Conversation(newId)) {
                        popUpTo<Conversation> { inclusive = true }
                    }
                },
                onSwitchProject = { navController.popBackStack(Projects, inclusive = false) },
            )
        }
    }
}
