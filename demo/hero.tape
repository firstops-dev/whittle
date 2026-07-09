Output demo/hero.gif
Set Theme "Catppuccin Mocha"
Set FontSize 18
Set Width 1000
Set Height 600
Set Padding 24
Set TypingSpeed 45ms
Set CursorBlink false

# Define whittle as this session's replay, hidden, so the visible command reads
# as the real subcommand (`whittle watch`) driving whittle's own captured output.
Hide
Type "whittle() { sh demo/hero-feed.sh; }; clear"
Enter
Show

Sleep 500ms
Type "whittle watch"
Enter
Sleep 24s
