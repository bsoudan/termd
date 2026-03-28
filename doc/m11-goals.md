# Milestone 11 Goals — Model Decomposition

## 1. Component architecture

Decompose the monolithic `Model` into self-contained components, each with its own state,
`Update()`, and `View()`. The parent model routes messages based on the active mode and
composes the views.

## 2. Clean boundaries

Each component should:
- Own all its state (no reaching into sibling state)
- Handle only the messages relevant to it
- Render only its portion of the screen
- Communicate with the parent via return values and typed messages, not shared fields

## 3. Prepare for multi-region

The terminal component should be parameterized by region, making it straightforward to
have multiple terminal instances (tabs) in a future milestone, each with their own screen,
cursor, scrollback, and mouse mode state.
