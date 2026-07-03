package net.anzix.acpp.ui.nav

import kotlinx.serialization.Serializable

// Type-safe Navigation-Compose destinations. The three navigation levels are
// Projects -> Conversations -> Conversation, plus a first-run Setup screen.
sealed interface Destination

@Serializable
data object Setup : Destination

@Serializable
data object Projects : Destination

@Serializable
data class Conversations(val projectName: String) : Destination

@Serializable
data class Conversation(val sessionId: String) : Destination
