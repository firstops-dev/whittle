Output demo/whittle.gif
Set FontSize 17
Set Width 1040
Set Height 600
Set Padding 22
Set Theme "Catppuccin Mocha"
Set TypingSpeed 38ms

Type "# a noisy build log: 144 lines, 2 real errors buried in the middle"
Enter
Sleep 900ms
Type "wc -l demo/build.log"
Enter
Sleep 1400ms
Type "whittle compress demo/build.log"
Enter
Sleep 2600ms
Type "whittle compress -stats demo/build.log > /dev/null"
Enter
Sleep 2400ms
Type "# both errors kept, 140 INFO lines -> one honest marker, byte-for-byte the rest"
Enter
Sleep 2600ms
