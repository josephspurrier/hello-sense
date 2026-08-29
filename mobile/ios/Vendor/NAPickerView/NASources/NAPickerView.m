//
//  NAPickerView.m
//  NAPickerView
//
//  Created by iNghia on 8/4/13.
//  Copyright (c) 2013 nghialv. All rights reserved.
//

#import "NAPickerView.h"
#import <QuartzCore/QuartzCore.h>

@interface NAPickerView() <UITableViewDataSource, UITableViewDelegate>

@property (strong, nonatomic) NSArray *items;
@property (strong, nonatomic) NSString *cellClassName;
@property (strong, nonatomic) UITableView *tableView;
@property (strong, nonatomic) NSIndexPath *currentIndex;
@property (strong, nonatomic) UIView *overlay;

@end

@implementation NAPickerView
@synthesize showOverlay = mShowOverlay;

- (id)initWithFrame:(CGRect)frame
           andItems:(NSArray *)items
   andCellClassName:(NSString *)className
        andDelegate:(id)delegate
{
    self = [super initWithFrame:frame];
    if (self) {
        // Initialization code
        [self configWithFrame:frame
                     andItems:items
             andCellClassName:className
                  andDelegate:delegate];
    }
    return self;
}

- (id)initWithFrame:(CGRect)frame
           andItems:(NSArray *)items
        andDelegate:(id)delegate
{
    self = [super initWithFrame:frame];
    if (self) {
        // Initialization code
        [self configWithFrame:frame
                     andItems:items
             andCellClassName:@"NALabelCell"
                  andDelegate:delegate];
    }
    return self;
}

- (void)configWithFrame:(CGRect)frame
               andItems:(NSArray *)items
       andCellClassName:(NSString *)className
            andDelegate:(id)delegate
{
    self.items = items;
    self.delegate = delegate;
    self.cellClassName = className;
    self.cellHeight = 70;
    
    self.tableView = [[UITableView alloc] initWithFrame:self.bounds style:UITableViewStylePlain];
    // Exact geometry, not estimates.
    //
    // scrollToRowAtIndexPath:...Middle is what centres the selected value, and
    // with estimated heights UITableView computes that target from estimates for
    // every row it has not laid out yet, then corrects as cells come into view.
    // Over the INT16_MAX rows infinite scrolling uses, the leftover error is a
    // fraction of a cell and it differs per column, because each column's
    // selected row sits at a different depth: hours land near row 1200, minutes
    // near 6000. The columns then disagree by that difference and the clock
    // reads out of line. Estimation only exists to avoid measuring every row and
    // every row here is a fixed 70pt, so there is nothing to save.
    // A picker column centres itself with its own header and footer. Letting
    // UIKit add safe-area insets on top of that shifts the selected row off
    // centre by the size of the inset.
    if (@available(iOS 11.0, *)) {
        self.tableView.contentInsetAdjustmentBehavior = UIScrollViewContentInsetAdjustmentNever;
    }
    self.tableView.estimatedRowHeight = 0;
    self.tableView.estimatedSectionHeaderHeight = 0;
    self.tableView.estimatedSectionFooterHeight = 0;
    self.tableView.delegate = self;
    self.tableView.dataSource = self;
    self.tableView.separatorStyle = UITableViewCellSeparatorStyleNone;
    self.tableView.showsHorizontalScrollIndicator = NO;
    self.tableView.showsVerticalScrollIndicator = NO;
    self.tableView.allowsSelection = NO;
    self.tableView.opaque = NO;
    self.tableView.backgroundColor = [UIColor clearColor];
    [self addSubview:self.tableView];
    
    self.currentIndex = [NSIndexPath indexPathForRow:0 inSection:0];
    self.backgroundColor = [UIColor grayColor];
    
    mShowOverlay = NO;
    self.showOverlay = YES;
    
    self.configureBlock = ^(NALabelCell *cell, NSString *item) {
        [cell.textView setText:item];
        cell.textView.textAlignment = NSTextAlignmentCenter;
        cell.textView.font = [UIFont systemFontOfSize:30];
        cell.textView.backgroundColor = [UIColor clearColor];
        cell.textView.textColor = [UIColor grayColor];
    };
    
    self.highlightBlock = ^(NALabelCell *cell) {
        cell.textView.textColor = [UIColor greenColor];
    };
    
    self.unhighlightBlock = ^(NALabelCell *cell) {
        cell.textView.textColor = [UIColor grayColor];
    };
}

- (void)layoutSubviews
{
    [super layoutSubviews];
    CGRect tableFrame = self.tableView.frame;
    CGFloat height = CGRectGetHeight(self.bounds);
    BOOL heightChanged = fabs(CGRectGetHeight(tableFrame) - height) > 0.5;
    tableFrame.size.height = height;
    self.tableView.frame = tableFrame;
    self.tableView.backgroundColor = [UIColor clearColor];
    self.tableView.separatorStyle = UITableViewCellSeparatorStyleNone;

    // Re-centre whenever the geometry moves.
    //
    // headerHeight is derived from bounds, and every column is built at the
    // storyboard's height before being laid out at the device's, so the header
    // the table cached is the wrong size until it is asked again.
    //
    // Columns that scroll hide this, because scrollToRow re-centres them on
    // every setIndex. A column that never scrolls does not: setIndex returns
    // early when the index is unchanged, so a 2-item meridiem selected on AM
    // (row 0, the value it is already on) never scrolls once, and sits wherever
    // the stale header left it. That is why AM and PM drifted below the digits
    // while hours and minutes agreed with each other.
    //
    // Re-centring here fixes the other half of it too: with content only one
    // cell taller than the frame, PM could never be scrolled to the middle by
    // setIndex alone.
    if (heightChanged) {
        [self.tableView reloadData];
        [self.tableView layoutIfNeeded];
        [self.tableView scrollToRowAtIndexPath:self.currentIndex
                              atScrollPosition:UITableViewScrollPositionMiddle
                                      animated:NO];
    }
}

- (CGFloat)headerHeight
{
    return self.bounds.size.height/2 - [self cellHeight]/2;
}

- (void)setBorderWidth:(CGFloat)borderWidth
{
    self.layer.borderWidth = borderWidth;
}

- (void)setBorderColor:(UIColor *)borderColor
{
    [self.layer setBorderColor:[borderColor CGColor]];
}

- (void)setCornerRadius:(CGFloat)cornerRadius
{
    self.layer.cornerRadius = cornerRadius;
    self.layer.masksToBounds = YES;
}

- (void)setIndex:(NSInteger)index
{
    [self setIndex:index animated:NO];
}

- (void)setIndex:(NSInteger)index animated:(BOOL)animated
{
    if (self.items.count == 0) {
        return;
    }
    
    NSInteger adjustedIndex = self.infiniteScrolling ? index + (self.items.count * 100) : index;
    if (adjustedIndex == self.currentIndex.row)
        return;
    
    self.currentIndex = [NSIndexPath indexPathForItem:adjustedIndex inSection:0];
    
    __weak typeof(self) weakSelf = self;
    dispatch_async(dispatch_get_main_queue(), ^{
        [weakSelf.tableView scrollToRowAtIndexPath:weakSelf.currentIndex
                                  atScrollPosition:UITableViewScrollPositionMiddle
                                          animated:animated];
    });
}


- (void)setShowOverlay:(BOOL)showOverlay
{
    if (showOverlay != mShowOverlay) {
        mShowOverlay = showOverlay;
        [self setOverlayView];
    }
}

- (void)setOverlayColor:(UIColor *)overlayColor
{
    if (![_overlayColor isEqual:overlayColor]) {
        _overlayColor = overlayColor;
        self.overlay.backgroundColor = overlayColor;
    }
}

- (void)setOverlayView
{
    if (self.showOverlay) {
        self.overlay = [[UIView alloc] initWithFrame:CGRectMake(0,
                                                                self.frame.size.height/2 - [self cellHeight]/2,
                                                                self.frame.size.width,
                                                                [self cellHeight])];
        self.overlay.backgroundColor = self.overlayColor ?: [UIColor grayColor];
        self.overlay.alpha = 0.5;
        self.overlay.userInteractionEnabled = NO;
        [self addSubview:self.overlay];
    } else {
        [self.overlay removeFromSuperview];
    }
}

#pragma mark - UITableView delegate
- (CGFloat)tableView:(UITableView *)tableView heightForHeaderInSection:(NSInteger)section
{
    return [self headerHeight];
}

- (UIView *)tableView:(UITableView *)tableView viewForHeaderInSection:(NSInteger)section
{
    UIView *emptyHeader = [[UIView alloc] initWithFrame:CGRectMake(0,
                                                                   0,
                                                                   self.tableView.frame.size.width,
                                                                   [self headerHeight])];
    emptyHeader.backgroundColor = [UIColor clearColor];
    return emptyHeader;
}

- (CGFloat)tableView:(UITableView *)tableView heightForFooterInSection:(NSInteger)section
{
    return [self headerHeight];
}

- (UIView *)tableView:(UITableView *)tableView viewForFooterInSection:(NSInteger)section
{
    UIView *emptyHeader = [[UIView alloc] initWithFrame:CGRectMake(0,
                                                                   0,
                                                                   self.tableView.frame.size.width,
                                                                   [self headerHeight])];
    emptyHeader.backgroundColor = [UIColor clearColor];
    return emptyHeader;
}

- (CGFloat)tableView:(UITableView *)tableView heightForRowAtIndexPath:(NSIndexPath *)indexPath
{
	return [self cellHeight];
}

- (NSInteger)tableView:(UITableView *)tableView numberOfRowsInSection:(NSInteger)section
{
    return self.infiniteScrolling ? INT16_MAX : [self.items count];
}

- (UITableViewCell *)tableView:(UITableView *)tableView cellForRowAtIndexPath:(NSIndexPath *)indexPath
{
    static NSString *cellIdentifier = @"NACellIdentifier";
    NAPickerCell *cell = [tableView dequeueReusableCellWithIdentifier:cellIdentifier];
    
    if(!cell) {
        cell = [[NSClassFromString(self.cellClassName) alloc] initWithStyle:UITableViewCellStyleDefault
                                                            reuseIdentifier:cellIdentifier
                                                                  cellWidth:self.tableView.bounds.size.width];
    }
    NSInteger actualRow = indexPath.row % self.items.count;
    self.configureBlock(cell, [self.items objectAtIndex:actualRow]);
    if(indexPath.row == self.currentIndex.row) {
        if (self.highlightBlock)
            self.highlightBlock(cell);
    } else if (self.unhighlightBlock) {
        self.unhighlightBlock(cell);
    }
    return cell;
}

#pragma mark - ScrollViewDelegate
- (void)scrollViewWillEndDragging:(UIScrollView *)scrollView
                     withVelocity:(CGPoint)velocity
              targetContentOffset:(inout CGPoint *)targetContentOffset
{
    GLfloat rowHeight = [self cellHeight];
    CGFloat floatVal = targetContentOffset->y / rowHeight;
	NSInteger rounded = (NSInteger)(lround(floatVal));
	targetContentOffset->y = rounded * rowHeight;
    [self.delegate didSelectedAtIndexDel:rounded % self.items.count];
}

- (void)scrollViewDidScroll:(UIScrollView *)scrollView
{
    NSArray *visibleIndex = [self.tableView indexPathsForVisibleRows];
    NSArray *visibleIndexSorted = [visibleIndex sortedArrayUsingComparator:^NSComparisonResult(id a, id b){
        CGRect r1 = [self.tableView rectForRowAtIndexPath:(NSIndexPath *)a];
        CGRect r2 = [self.tableView rectForRowAtIndexPath:(NSIndexPath *)b];
        CGFloat y1 = fabs(r1.origin.y + r1.size.height/2 - self.tableView.contentOffset.y - self.tableView.center.y);
        CGFloat y2 = fabs(r2.origin.y + r2.size.height/2 - self.tableView.contentOffset.y - self.tableView.center.y);
        if (y1 > y2) {
            return NSOrderedDescending;
        }
        return NSOrderedAscending;
    }];
    
    NSIndexPath *middleIndex = [visibleIndexSorted objectAtIndex:0];
    if (!self.currentIndex) {
        self.currentIndex = middleIndex;
    }
    if (self.currentIndex.row == middleIndex.row) {
        return;
    }
    
    NAPickerCell *currentCell = (NAPickerCell *)[self.tableView cellForRowAtIndexPath:self.currentIndex];
    if (self.unhighlightBlock)
        self.unhighlightBlock(currentCell);
    NAPickerCell *middleCell = (NAPickerCell *)[self.tableView cellForRowAtIndexPath:middleIndex];
    if (self.highlightBlock)
        self.highlightBlock(middleCell);
    self.currentIndex = middleIndex;
}

- (void)dealloc {
    [_tableView setDelegate:nil];
    [_tableView setDataSource:nil];
}

@end
